package utils

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var _ io.ReadWriteCloser = &RequestWrapper{}

// requestWrapperIdleTimeout 接管后的连接在两个方向都无数据时的最长保持时间，防止僵死会话长期占用 NAT 名额
const requestWrapperIdleTimeout = 30 * time.Minute

type RequestWrapper struct {
	req    *http.Request
	reader *bytes.Buffer
	writer net.Conn
}

func NewRequestWrapper(req *http.Request, writer gin.ResponseWriter) (*RequestWrapper, error) {
	// 先在 Hijack 前读完请求体：此时仍受外层请求体大小与读超时限制，Hijack 后读超时会被清零
	buf := bytes.NewBuffer(nil)
	if err := req.Write(buf); err != nil {
		return nil, err
	}
	conn, _, err := writer.Hijack()
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(requestWrapperIdleTimeout))
	return &RequestWrapper{
		req:    req,
		reader: buf,
		writer: conn,
	}, nil
}

func (rw *RequestWrapper) Read(p []byte) (int, error) {
	if rw.reader != nil {
		count, err := rw.reader.Read(p)
		if err == nil {
			return count, nil
		}
		if err != io.EOF {
			return count, err
		}
		// 请求数据已全部转发，释放缓冲（最多 8MiB），避免整个会话期间常驻内存
		rw.reader = nil
	}
	// request 数据读完之后等待客户端断开连接或 grpc 超时
	count, err := rw.writer.Read(p)
	if count > 0 {
		rw.refreshDeadline()
	}
	return count, err
}

func (rw *RequestWrapper) Write(p []byte) (int, error) {
	count, err := rw.writer.Write(p)
	if count > 0 {
		rw.refreshDeadline()
	}
	return count, err
}

// refreshDeadline 任一方向有数据即顺延读写期限；对挂起中的读写同样生效
func (rw *RequestWrapper) refreshDeadline() {
	rw.writer.SetDeadline(time.Now().Add(requestWrapperIdleTimeout))
}

func (rw *RequestWrapper) Close() error {
	rw.req.Body.Close()
	rw.writer.Close()
	return nil
}
