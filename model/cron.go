package model

import (
	"time"

	"github.com/robfig/cron/v3"
	"github.com/xos/serverstatus/pkg/utils"
	"gorm.io/gorm"
)

const (
	CronCoverIgnoreAll = iota
	CronCoverAll
)

type Cron struct {
	Common
	Name           string
	Scheduler      string //分钟 小时 天 月 星期
	Command        string
	Servers        []uint64  `gorm:"-"`
	PushSuccessful bool      // 推送成功的通知
	LastExecutedAt time.Time // 最后一次执行时间
	LastResult     bool      // 最后一次执行结果
	Cover          uint8     // 计划任务覆盖范围 (0:仅覆盖特定服务器 1:仅忽略特定服务器)

	CronJobID  cron.EntryID `gorm:"-"`
	ServersRaw string
}

func (c *Cron) AfterFind(tx *gorm.DB) error {
	return utils.Json.Unmarshal([]byte(c.ServersRaw), &c.Servers)
}
