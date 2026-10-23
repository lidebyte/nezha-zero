package model

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"

	"github.com/railzen/nezha-zero/pkg/utils"
	pb "github.com/railzen/nezha-zero/proto"
	"gorm.io/gorm"
)

const TaskDispatchSlotLimit = 32

const taskSendTimeout = 10 * time.Second

var errTaskSendTimeout = errors.New("send task timeout; task stream evicted")

type taskStreamAbortKey struct{}

// WithTaskStreamAbort 为端口复用模式的 gRPC 请求注入中断函数。
// 该模式下 gRPC 走 ServeHTTP，写操作在 HTTP 处理协程内执行，流控阻塞时仅让 RequestTask 返回无法解除；
// 超时驱逐时调用 abort 中断该请求，阻塞中的 Send 和底层写入才会退出。
func WithTaskStreamAbort(ctx context.Context, abort func()) context.Context {
	return context.WithValue(ctx, taskStreamAbortKey{}, abort)
}

// CloseTaskChan 幂等关闭任务流关闭通道。
// 调用方必须持有对应的 TaskCloseLock；该通道只用于关闭通知，不传输数据。
func CloseTaskChan(ch chan error) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

type Server struct {
	Common
	Name         string
	Tag          string   // 分组名
	Secret       string   `gorm:"uniqueIndex" json:"-"`
	Note         string   `json:"-"`                    // 管理员可见备注
	PublicNote   string   `json:"PublicNote,omitempty"` // 公开备注
	Link         string   `json:"-"`                    // 订阅页跳转链接，不进入公开状态
	DisplayIndex int      // 展示排序，越大越靠前
	HideForGuest bool     // 对游客隐藏
	EnableDDNS   bool     // 启用DDNS
	DDNSProfiles []uint64 `gorm:"-" json:"-"` // DDNS配置

	DDNSProfilesRaw string `gorm:"default:'[]';column:ddns_profiles_raw" json:"-"`

	Host       *Host      `gorm:"-"`
	State      *HostState `gorm:"-"`
	LastActive time.Time  `gorm:"-"`

	TaskClose        chan error                        `gorm:"-" json:"-"`
	TaskCloseLock    *sync.Mutex                       `gorm:"-" json:"-"`
	TaskSendLock     *sync.Mutex                       `gorm:"-" json:"-"`
	TaskDispatchLock *sync.Mutex                       `gorm:"-" json:"-"`
	TaskDispatchSem  chan struct{}                     `gorm:"-" json:"-"`
	TaskStream       pb.NezhaService_RequestTaskServer `gorm:"-" json:"-"`
	RuntimeLock      *sync.RWMutex                     `gorm:"-" json:"-"`

	PrevTransferInSnapshot  uint64 `gorm:"-" json:"-"` // 上次数据点时的入站使用量
	PrevTransferOutSnapshot uint64 `gorm:"-" json:"-"` // 上次数据点时的出站使用量
}

func (s *Server) CopyFromRunningServer(old *Server) {
	if old == nil {
		return
	}
	host, state, lastActive, prevIn, prevOut := old.RuntimeSnapshot()
	s.Host = host
	s.State = state
	s.LastActive = lastActive
	s.TaskClose = old.TaskClose
	s.TaskCloseLock = old.TaskCloseLock
	s.TaskSendLock = old.TaskSendLock
	s.TaskDispatchLock = old.TaskDispatchLock
	s.TaskDispatchSem = old.TaskDispatchSem
	s.TaskStream = old.TaskStream
	s.RuntimeLock = old.RuntimeLock
	s.PrevTransferInSnapshot = prevIn
	s.PrevTransferOutSnapshot = prevOut
}

func (s *Server) InitRuntimeLock() {
	if s.RuntimeLock == nil {
		s.RuntimeLock = new(sync.RWMutex)
	}
}

func (s *Server) RuntimeSnapshot() (*Host, *HostState, time.Time, uint64, uint64) {
	if s == nil {
		return nil, nil, time.Time{}, 0, 0
	}
	s.InitRuntimeLock()
	s.RuntimeLock.RLock()
	defer s.RuntimeLock.RUnlock()

	var host *Host
	if s.Host != nil {
		hostValue := *s.Host
		if hostValue.CPU != nil {
			hostValue.CPU = append([]string(nil), hostValue.CPU...)
		}
		if hostValue.GPU != nil {
			hostValue.GPU = append([]string(nil), hostValue.GPU...)
		}
		host = &hostValue
	}
	var state *HostState
	if s.State != nil {
		stateValue := *s.State
		if stateValue.Temperatures != nil {
			stateValue.Temperatures = append([]SensorTemperature(nil), stateValue.Temperatures...)
		}
		state = &stateValue
	}
	return host, state, s.LastActive, s.PrevTransferInSnapshot, s.PrevTransferOutSnapshot
}

func (s *Server) SendTask(task *pb.Task) error {
	return s.sendTaskWithTimeout(task, taskSendTimeout)
}

// sendTaskWithTimeout 限制单次任务下发时间。超时通过关闭对应的 TaskClose
// 让 RequestTask 返回并取消 gRPC 流，确保底层 Send 也能退出，而不只是让调用方超时。
func (s *Server) sendTaskWithTimeout(task *pb.Task, timeout time.Duration) error {
	if s == nil {
		return errors.New("server is nil")
	}
	if s.TaskCloseLock == nil {
		return errors.New("task stream lock is nil")
	}
	if s.TaskSendLock == nil {
		return errors.New("task stream send lock is nil")
	}

	s.TaskSendLock.Lock()
	defer s.TaskSendLock.Unlock()

	s.TaskCloseLock.Lock()
	taskStream := s.TaskStream
	taskClose := s.TaskClose
	s.TaskCloseLock.Unlock()

	if taskStream == nil {
		return errors.New("task stream is nil")
	}
	if taskClose == nil {
		return errors.New("task close channel is nil")
	}

	var completionMu sync.Mutex
	completed, evicted := false, false
	timer := time.AfterFunc(timeout, func() {
		completionMu.Lock()
		defer completionMu.Unlock()
		if completed {
			return
		}

		s.TaskCloseLock.Lock()
		// 发送期间流可能已正常关闭或被新连接替换，不能误关新流。
		if s.TaskStream == taskStream && s.TaskClose == taskClose {
			CloseTaskChan(taskClose)
			s.TaskClose = nil
			s.TaskStream = nil
			evicted = true
		}
		s.TaskCloseLock.Unlock()

		// 捕获的旧流无论是否已被替换都要中断：复用模式下旧流的阻塞写不会随 RequestTask 返回而解除。
		if abort, ok := taskStream.Context().Value(taskStreamAbortKey{}).(func()); ok {
			abort()
		}
	})

	err := taskStream.Send(task)
	timer.Stop()

	completionMu.Lock()
	completed = true
	timedOut := evicted
	completionMu.Unlock()
	if timedOut {
		return errTaskSendTimeout
	}
	return err
}

func (s *Server) AfterFind(tx *gorm.DB) error {
	if s.DDNSProfilesRaw != "" {
		if err := utils.Json.Unmarshal([]byte(s.DDNSProfilesRaw), &s.DDNSProfiles); err != nil {
			log.Println("NEZHA>> Server.AfterFind:", err)
			return nil
		}
	}
	return nil
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ShowCountryFlag 与主界面逻辑一致
func (s *Server) ShowCountryFlag() bool {
	return s.State != nil && s.State.Uptime > 0
}

func (s Server) MarshalForDashboard() template.JS {
	name, _ := utils.Json.Marshal(s.Name)
	tag, _ := utils.Json.Marshal(s.Tag)
	note, _ := utils.Json.Marshal(s.Note)
	secret, _ := utils.Json.Marshal(s.Secret)
	ddnsProfilesRaw, _ := utils.Json.Marshal(s.DDNSProfilesRaw)
	publicNote, _ := utils.Json.Marshal(s.PublicNote)
	link, _ := utils.Json.Marshal(s.Link)
	return template.JS(fmt.Sprintf(`{"ID":%d,"Name":%s,"Secret":%s,"DisplayIndex":%d,"Tag":%s,"Note":%s,"HideForGuest": %s,"EnableDDNS": %s,"DDNSProfilesRaw": %s,"PublicNote": %s,"Link": %s}`, s.ID, name, secret, s.DisplayIndex, tag, note, boolToString(s.HideForGuest), boolToString(s.EnableDDNS), ddnsProfilesRaw, publicNote, link))
}
