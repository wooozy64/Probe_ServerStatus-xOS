package model

import (
	"fmt"
	"log"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/pkg/utils"
	pb "github.com/xos/serverstatus/proto"
	"gorm.io/gorm"
)

const (
	_ = iota
	TaskTypeCommand
	TaskTypeTerminal
	TaskTypeUpgrade
	TaskTypeKeepalive
)

type TerminalTask struct {
	// websocket 主机名
	Host string `json:"host,omitempty"`
	// 是否启用 SSL
	UseSSL bool `json:"use_ssl,omitempty"`
	// 会话标识
	Session string `json:"session,omitempty"`
	// Agent在连接Server时需要的额外Cookie信息
	Cookie string `json:"cookie,omitempty"`
}

const (
	MonitorCoverAll = iota
	MonitorCoverIgnoreAll
)

type Monitor struct {
	Common
	Name            string
	Type            uint8
	Target          string
	SkipServersRaw  string
	Duration        uint64
	Notify          bool
	NotificationTag string // 当前服务监控所属的通知组
	Cover           uint8

	SkipServers map[uint64]bool `gorm:"-" json:"-"`
	CronJobID   cron.EntryID    `gorm:"-" json:"-"`
}

func (m *Monitor) PB() *pb.Task {
	return &pb.Task{
		Id:   m.ID,
		Type: uint64(m.Type),
		Data: m.Target,
	}
}

// CronSpec 返回服务监控请求间隔对应的 cron 表达式
func (m *Monitor) CronSpec() string {
	if m.Duration == 0 {
		// 默认间隔 30 秒
		m.Duration = 30
	}
	return fmt.Sprintf("@every %ds", m.Duration)
}

func (m *Monitor) AfterFind(tx *gorm.DB) error {
	m.SkipServers = make(map[uint64]bool)
	var skipServers []uint64
	if err := utils.Json.Unmarshal([]byte(m.SkipServersRaw), &skipServers); err != nil {
		log.Println("NG>> Monitor.AfterFind:", err)
		return nil
	}
	for i := 0; i < len(skipServers); i++ {
		m.SkipServers[skipServers[i]] = true
	}
	return nil
}

func (m *Monitor) InitSkipServers() error {
	var skipServers []uint64
	if err := utils.Json.Unmarshal([]byte(m.SkipServersRaw), &skipServers); err != nil {
		return err
	}
	m.SkipServers = make(map[uint64]bool)
	for i := 0; i < len(skipServers); i++ {
		m.SkipServers[skipServers[i]] = true
	}
	return nil
}
