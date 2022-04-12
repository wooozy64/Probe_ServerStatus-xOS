package model

import (
	pb "github.com/xos/serverstatus/proto"
)

// MonitorHistory 历史监控记录
type MonitorHistory struct {
	Common
	MonitorID  uint64
	Delay      float32 // 延迟，毫秒
	Data       string
	Successful bool // 是否成功
}

func PB2MonitorHistory(r *pb.TaskResult) MonitorHistory {
	return MonitorHistory{
		Delay:      r.GetDelay(),
		Successful: r.GetSuccessful(),
		MonitorID:  r.GetId(),
		Data:       r.GetData(),
	}
}
