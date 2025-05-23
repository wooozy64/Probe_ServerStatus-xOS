package rpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/xos/serverstatus/pkg/ddns"
	"github.com/xos/serverstatus/pkg/geoip"
	"github.com/xos/serverstatus/pkg/grpcx"
	"github.com/xos/serverstatus/pkg/utils"

	"github.com/jinzhu/copier"
	"github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/xos/serverstatus/model"
	pb "github.com/xos/serverstatus/proto"
	"github.com/xos/serverstatus/service/singleton"
)

var ServerHandlerSingleton *ServerHandler

type ServerHandler struct {
	Auth          *authHandler
	ioStreams     map[string]*ioStreamContext
	ioStreamMutex *sync.RWMutex
}

func NewServerHandler() *ServerHandler {
	return &ServerHandler{
		Auth:          &authHandler{},
		ioStreamMutex: new(sync.RWMutex),
		ioStreams:     make(map[string]*ioStreamContext),
	}
}

func (s *ServerHandler) ReportTask(c context.Context, r *pb.TaskResult) (*pb.Receipt, error) {
	var err error
	var clientID uint64
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	if r.GetType() == model.TaskTypeCommand {
		// 处理上报的计划任务
		singleton.CronLock.RLock()
		defer singleton.CronLock.RUnlock()
		cr := singleton.Crons[r.GetId()]
		if cr != nil {
			singleton.ServerLock.RLock()
			defer singleton.ServerLock.RUnlock()
			// 保存当前服务器状态信息
			curServer := model.Server{}
			copier.Copy(&curServer, singleton.ServerList[clientID])
			if cr.PushSuccessful && r.GetSuccessful() {
				singleton.SendNotification(cr.NotificationTag, fmt.Sprintf("[%s] %s, %s\n%s", singleton.Localizer.MustLocalize(
					&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedSuccessfully",
					},
				), cr.Name, singleton.ServerList[clientID].Name, r.GetData()), nil, &curServer)
			}
			if !r.GetSuccessful() {
				singleton.SendNotification(cr.NotificationTag, fmt.Sprintf("[%s] %s, %s\n%s", singleton.Localizer.MustLocalize(
					&i18n.LocalizeConfig{
						MessageID: "ScheduledTaskExecutedFailed",
					},
				), cr.Name, singleton.ServerList[clientID].Name, r.GetData()), nil, &curServer)
			}
			singleton.DB.Model(cr).Updates(model.Cron{
				LastExecutedAt: time.Now().Add(time.Second * -1 * time.Duration(r.GetDelay())),
				LastResult:     r.GetSuccessful(),
			})
		}
	} else if model.IsServiceSentinelNeeded(r.GetType()) {
		singleton.ServiceSentinelShared.Dispatch(singleton.ReportData{
			Data:     r,
			Reporter: clientID,
		})
	}
	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) RequestTask(h *pb.Host, stream pb.ServerService_RequestTaskServer) error {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(stream.Context()); err != nil {
		return err
	}
	closeCh := make(chan error)
	singleton.ServerLock.RLock()
	singleton.ServerList[clientID].TaskCloseLock.Lock()
	// 修复不断的请求 task 但是没有 return 导致内存泄漏
	if singleton.ServerList[clientID].TaskClose != nil {
		close(singleton.ServerList[clientID].TaskClose)
	}
	singleton.ServerList[clientID].TaskStream = stream
	singleton.ServerList[clientID].TaskClose = closeCh
	singleton.ServerList[clientID].TaskCloseLock.Unlock()
	singleton.ServerLock.RUnlock()
	return <-closeCh
}

func (s *ServerHandler) ReportSystemState(c context.Context, r *pb.State) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	state := model.PB2State(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	// 更新服务器在线状态
	singleton.ServerList[clientID].IsOnline = true
	singleton.ServerList[clientID].LastActive = time.Now()

	// 更新当前上报的流量为累计值
	// 将状态中的流量与数据库中存储的累计流量合并
	if singleton.ServerList[clientID].CumulativeNetInTransfer > 0 {
		log.Printf("NG>> 服务器 %s 开始累计流量: 入站累计 %d + 当前 %d",
			singleton.ServerList[clientID].Name,
			singleton.ServerList[clientID].CumulativeNetInTransfer,
			state.NetInTransfer)
	}

	// 输出完整状态数据，用于调试
	log.Printf("NG>> 服务器 %s 状态上报: CPU:%.2f%% 内存:%d 硬盘:%d 进程:%d",
		singleton.ServerList[clientID].Name,
		state.CPU,
		state.MemUsed,
		state.DiskUsed,
		state.ProcessCount)

	// 原始报告流量保存用于增量计算
	originalNetInTransfer := state.NetInTransfer
	originalNetOutTransfer := state.NetOutTransfer

	// 将当前状态与数据库中累计的流量合并
	state.NetInTransfer += singleton.ServerList[clientID].CumulativeNetInTransfer
	state.NetOutTransfer += singleton.ServerList[clientID].CumulativeNetOutTransfer

	// 保存当前状态
	singleton.ServerList[clientID].State = &state

	// 保存最后状态，用于离线后显示
	lastState := model.HostState{}
	copier.Copy(&lastState, &state)
	singleton.ServerList[clientID].LastStateBeforeOffline = &lastState

	// 也将当前状态保存到数据库中的LastStateJSON字段，用于面板重启后恢复离线机器状态
	lastStateJSON, err := utils.Json.Marshal(lastState)
	if err == nil {
		singleton.ServerList[clientID].LastStateJSON = string(lastStateJSON)
		singleton.ServerList[clientID].LastOnline = singleton.ServerList[clientID].LastActive

		// 立即更新到数据库
		singleton.DB.Model(singleton.ServerList[clientID]).Updates(map[string]interface{}{
			"last_state_json": singleton.ServerList[clientID].LastStateJSON,
			"last_online":     singleton.ServerList[clientID].LastOnline,
		})

		log.Printf("NG>> 服务器 %s 最后状态已保存到数据库", singleton.ServerList[clientID].Name)
	}

	// 应对 dashboard 重启的情况，如果从未记录过，先打点，等到小时时间点时入库
	if singleton.ServerList[clientID].PrevTransferInSnapshot == 0 || singleton.ServerList[clientID].PrevTransferOutSnapshot == 0 {
		singleton.ServerList[clientID].PrevTransferInSnapshot = int64(originalNetInTransfer)
		singleton.ServerList[clientID].PrevTransferOutSnapshot = int64(originalNetOutTransfer)
	}

	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) ReportSystemInfo(c context.Context, r *pb.Host) (*pb.Receipt, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}
	host := model.PB2Host(r)
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()

	// 检查并更新DDNS
	if singleton.ServerList[clientID].EnableDDNS && host.IP != "" &&
		(singleton.ServerList[clientID].Host == nil || singleton.ServerList[clientID].Host.IP != host.IP) {
		ipv4, ipv6, _ := utils.SplitIPAddr(host.IP)
		providers, err := singleton.GetDDNSProvidersFromProfiles(singleton.ServerList[clientID].DDNSProfiles, &ddns.IP{Ipv4Addr: ipv4, Ipv6Addr: ipv6})
		if err == nil {
			for _, provider := range providers {
				go func(provider *ddns.Provider) {
					provider.UpdateDomain(context.Background())
				}(provider)
			}
		} else {
			log.Printf("NG>> 获取DDNS配置时发生错误: %v", err)
		}
	}

	// 发送IP变动通知
	if singleton.ServerList[clientID].Host != nil && singleton.Conf.EnableIPChangeNotification &&
		((singleton.Conf.Cover == model.ConfigCoverAll && !singleton.Conf.IgnoredIPNotificationServerIDs[clientID]) ||
			(singleton.Conf.Cover == model.ConfigCoverIgnoreAll && singleton.Conf.IgnoredIPNotificationServerIDs[clientID])) &&
		singleton.ServerList[clientID].Host.IP != "" &&
		host.IP != "" &&
		singleton.ServerList[clientID].Host.IP != host.IP {

		singleton.SendNotification(singleton.Conf.IPChangeNotificationTag,
			fmt.Sprintf(
				"[%s] %s, %s => %s",
				singleton.Localizer.MustLocalize(&i18n.LocalizeConfig{
					MessageID: "IPChanged",
				}),
				singleton.ServerList[clientID].Name, singleton.IPDesensitize(singleton.ServerList[clientID].Host.IP),
				singleton.IPDesensitize(host.IP),
			),
			nil)
	}

	/**
	 * 这里的 singleton 中的数据都是关机前的旧数据
	 * 当 agent 重启时，bootTime 变大，agent 端会先上报 host 信息，然后上报 state 信息
	 * 这是可以借助上报顺序的空档，将停机前的流量统计数据标记下来，加到下一个小时的数据点上
	 */
	if singleton.ServerList[clientID].Host != nil && singleton.ServerList[clientID].Host.BootTime < host.BootTime {
		log.Printf("NG>> 检测到服务器 %s 重启，更新累计流量", singleton.ServerList[clientID].Name)

		// 服务器重启了，将当前的流量数据保存到累计数据中
		if singleton.ServerList[clientID].State != nil {
			// 计算累计流量数据
			newCumulativeIn := singleton.ServerList[clientID].State.NetInTransfer
			newCumulativeOut := singleton.ServerList[clientID].State.NetOutTransfer

			log.Printf("NG>> 服务器 %s 重启前累计流量: 入站 %d / 出站 %d",
				singleton.ServerList[clientID].Name,
				newCumulativeIn, newCumulativeOut)

			// 更新内存中累计流量
			singleton.ServerList[clientID].CumulativeNetInTransfer = newCumulativeIn
			singleton.ServerList[clientID].CumulativeNetOutTransfer = newCumulativeOut

			// 立即保存累计流量到数据库
			singleton.DB.Model(singleton.ServerList[clientID]).Updates(map[string]interface{}{
				"cumulative_net_in_transfer":  newCumulativeIn,
				"cumulative_net_out_transfer": newCumulativeOut,
			})

			log.Printf("NG>> 服务器 %s 累计流量已更新并保存到数据库", singleton.ServerList[clientID].Name)
		}

		singleton.ServerList[clientID].PrevTransferInSnapshot = 0
		singleton.ServerList[clientID].PrevTransferOutSnapshot = 0
	}

	// 不要冲掉国家码
	if singleton.ServerList[clientID].Host != nil {
		host.CountryCode = singleton.ServerList[clientID].Host.CountryCode
	}

	// 保存完整Host信息到数据库，用于重启后恢复
	hostJSON, err := utils.Json.Marshal(host)
	if err == nil {
		// 使用Replace语法，如果记录不存在则插入，存在则更新
		singleton.DB.Exec(`
			INSERT INTO last_reported_host (server_id, host_json) 
			VALUES (?, ?)
			ON CONFLICT(server_id) 
			DO UPDATE SET host_json = ?
		`, clientID, string(hostJSON), string(hostJSON))

		log.Printf("NG>> 服务器 %s Host信息已保存到数据库", singleton.ServerList[clientID].Name)
	}

	singleton.ServerList[clientID].Host = &host
	return &pb.Receipt{Proced: true}, nil
}

func (s *ServerHandler) IOStream(stream pb.ServerService_IOStreamServer) error {
	if _, err := s.Auth.Check(stream.Context()); err != nil {
		return err
	}
	id, err := stream.Recv()
	if err != nil {
		return err
	}
	if id == nil || len(id.Data) < 4 || (id.Data[0] != 0xff && id.Data[1] != 0x05 && id.Data[2] != 0xff && id.Data[3] == 0x05) {
		return fmt.Errorf("invalid stream id")
	}

	streamId := string(id.Data[4:])

	if _, err := s.GetStream(streamId); err != nil {
		return err
	}
	iw := grpcx.NewIOStreamWrapper(stream)
	if err := s.AgentConnected(streamId, iw); err != nil {
		return err
	}
	iw.Wait()
	return nil
}

func (s *ServerHandler) LookupGeoIP(c context.Context, r *pb.GeoIP) (*pb.GeoIP, error) {
	var clientID uint64
	var err error
	if clientID, err = s.Auth.Check(c); err != nil {
		return nil, err
	}

	// 根据内置数据库查询 IP 地理位置
	record := &geoip.IPInfo{}
	ip := r.GetIp()
	netIP := net.ParseIP(ip)
	location, err := geoip.Lookup(netIP, record)
	if err != nil {
		return nil, err
	}

	// 将地区码写入到 Host
	singleton.ServerLock.RLock()
	defer singleton.ServerLock.RUnlock()
	if singleton.ServerList[clientID].Host == nil {
		return nil, fmt.Errorf("host not found")
	}
	singleton.ServerList[clientID].Host.CountryCode = location

	return &pb.GeoIP{Ip: ip, CountryCode: location}, nil
}
