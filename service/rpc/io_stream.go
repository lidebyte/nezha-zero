package rpc

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type ioStreamContext struct {
	userIo           io.ReadWriteCloser
	agentIo          io.ReadWriteCloser
	userIoConnectCh  chan struct{}
	agentIoConnectCh chan struct{}
	createdAt        time.Time
}

type bp struct {
	buf []byte
}

var bufPool = sync.Pool{
	New: func() any {
		return &bp{
			buf: make([]byte, 1024*1024),
		}
	},
}

const (
	ioStreamCleanupInterval = time.Minute
	ioStreamConnectTimeout  = 5 * time.Minute
	ioStreamLimit           = 256
	// 首帧 stream id 等待上限：避免已鉴权 agent 占槽后永不 Recv 导致永久耗尽
	ioStreamIDTimeout = 2 * time.Minute
	// 终端空闲时 IOStream 上没有应用层数据，CF 等 CDN 的 Proxy Read Timeout（125s）会断流；
	// HTTP/2 PING 是逐跳的不会透传，需要面板主动向 agent 发数据保活
	terminalKeepaliveInterval = 30 * time.Second
)

// agent 终端接收循环只处理首字节 0(输入)/1(resize)，无 default 分支，
// 未知首字节会被新旧版本 agent 直接忽略；不能为空，空包会让 agent 退出终端
var terminalKeepalivePayload = []byte{0xff}

func (s *NezhaHandler) CreateStream(streamId string) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	if len(s.ioStreams) >= ioStreamLimit {
		return errors.New("too many active streams")
	}
	s.ioStreams[streamId] = &ioStreamContext{
		userIoConnectCh:  make(chan struct{}),
		agentIoConnectCh: make(chan struct{}),
		createdAt:        time.Now(),
	}
	return nil
}

func (s *NezhaHandler) GetStream(streamId string) (*ioStreamContext, error) {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx, nil
	}

	return nil, errors.New("stream not found")
}

func (s *NezhaHandler) CloseStream(streamId string) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		s.closeStreamLocked(streamId, ctx)
	}

	return nil
}

func (s *NezhaHandler) closeStreamLocked(streamId string, ctx *ioStreamContext) {
	if ctx.userIo != nil {
		ctx.userIo.Close()
	}
	if ctx.agentIo != nil {
		ctx.agentIo.Close()
	}
	delete(s.ioStreams, streamId)
}

func (s *NezhaHandler) cleanupStaleStreams() {
	ticker := time.NewTicker(ioStreamCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-ioStreamConnectTimeout)
		s.ioStreamMutex.Lock()
		for streamId, ctx := range s.ioStreams {
			if ctx.createdAt.Before(cutoff) && (ctx.userIo == nil || ctx.agentIo == nil) {
				s.closeStreamLocked(streamId, ctx)
			}
		}
		s.ioStreamMutex.Unlock()
	}
}

func (s *NezhaHandler) UserConnected(streamId string, userIo io.ReadWriteCloser) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	stream, ok := s.ioStreams[streamId]
	if !ok {
		return errors.New("stream not found")
	}
	if stream.userIo != nil {
		return errors.New("user already connected")
	}
	stream.userIo = userIo
	close(stream.userIoConnectCh)

	return nil
}

func (s *NezhaHandler) AgentConnected(streamId string, agentIo io.ReadWriteCloser) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	stream, ok := s.ioStreams[streamId]
	if !ok {
		return errors.New("stream not found")
	}
	if stream.agentIo != nil {
		return errors.New("agent already connected")
	}
	stream.agentIo = agentIo
	close(stream.agentIoConnectCh)

	return nil
}

func (s *NezhaHandler) StartStream(streamId string, timeout time.Duration) error {
	return s.startStream(streamId, timeout, false)
}

// StartTerminalStream 同 StartStream，额外定期向 agent 发送终端心跳；
// 仅适用于终端流，文件管理/NAT 的数据会被注入的心跳污染
func (s *NezhaHandler) StartTerminalStream(streamId string, timeout time.Duration) error {
	return s.startStream(streamId, timeout, true)
}

func (s *NezhaHandler) startStream(streamId string, timeout time.Duration, terminalKeepalive bool) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

LOOP:
	for {
		select {
		case <-timeoutTimer.C:
			break LOOP
		default:
		}

		select {
		case <-stream.userIoConnectCh:
			if stream.agentIo != nil {
				break LOOP
			}
		case <-stream.agentIoConnectCh:
			if stream.userIo != nil {
				break LOOP
			}
		case <-timeoutTimer.C:
			break LOOP
		}
		time.Sleep(time.Millisecond * 500)
	}

	if stream.userIo == nil && stream.agentIo == nil {
		return errors.New("timeout: no connection established")
	}
	if stream.userIo == nil {
		return errors.New("timeout: user connection not established")
	}
	if stream.agentIo == nil {
		return errors.New("timeout: agent connection not established")
	}

	isDone := new(atomic.Bool)
	endCh := make(chan struct{})
	doneErrCh := make(chan error, 1)

	go func() {
		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)
		_, innerErr := io.CopyBuffer(stream.userIo, stream.agentIo, bp.buf)
		if isDone.CompareAndSwap(false, true) {
			doneErrCh <- innerErr
			close(endCh)
		}
	}()
	go func() {
		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)
		_, innerErr := io.CopyBuffer(stream.agentIo, stream.userIo, bp.buf)
		if isDone.CompareAndSwap(false, true) {
			doneErrCh <- innerErr
			close(endCh)
		}
	}()
	if terminalKeepalive {
		go func() {
			ticker := time.NewTicker(terminalKeepaliveInterval)
			defer ticker.Stop()
			for {
				select {
				case <-endCh:
					return
				case <-ticker.C:
					if _, err := stream.agentIo.Write(terminalKeepalivePayload); err != nil {
						return
					}
				}
			}
		}()
	}

	<-endCh
	return <-doneErrCh
}
