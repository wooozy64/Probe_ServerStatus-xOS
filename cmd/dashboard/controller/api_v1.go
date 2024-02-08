package controller

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xos/serverstatus/pkg/mygin"
	"github.com/xos/serverstatus/service/singleton"
)

type apiV1 struct {
	r gin.IRouter
}

func (v *apiV1) serve() {
	r := v.r.Group("")
	// API
	r.Use(mygin.Authorize(mygin.AuthorizeOption{
		Member:   true,
		IsPage:   false,
		AllowAPI: true,
		Msg:      "访问此接口需要认证",
		Btn:      "点此登录",
		Redirect: "/login",
	}))
	r.GET("/server/list", v.serverList)
	r.GET("/server/details", v.serverDetails)
	mr := v.r.Group("monitor")
	mr.GET("/:id", v.monitorHistoriesById)
	mr.GET("/day/:id", v.monitorHistoriesDayById)
	mr.GET("/month/:id", v.monitorHistoriesMonthById)
	mr.GET("", v.monitorHistories)
}

// serverList 获取服务器列表 不传入Query参数则获取全部
// header: Authorization: Token
// query: tag (服务器分组)
func (v *apiV1) serverList(c *gin.Context) {
	tag := c.Query("tag")
	if tag != "" {
		c.JSON(200, singleton.ServerAPI.GetListByTag(tag))
		return
	}
	c.JSON(200, singleton.ServerAPI.GetAllList())
}

// serverDetails 获取服务器信息 不传入Query参数则获取全部
// header: Authorization: Token
// query: id (服务器ID，逗号分隔，优先级高于tag查询)
// query: tag (服务器分组)
func (v *apiV1) serverDetails(c *gin.Context) {
	var idList []uint64
	idListStr := strings.Split(c.Query("id"), ",")
	if c.Query("id") != "" {
		idList = make([]uint64, len(idListStr))
		for i, v := range idListStr {
			id, _ := strconv.ParseUint(v, 10, 64)
			idList[i] = id
		}
	}
	tag := c.Query("tag")
	if tag != "" {
		c.JSON(200, singleton.ServerAPI.GetStatusByTag(tag))
		return
	}
	if len(idList) != 0 {
		c.JSON(200, singleton.ServerAPI.GetStatusByIDList(idList))
		return
	}
	c.JSON(200, singleton.ServerAPI.GetAllStatus())
}

func (v *apiV1) monitorHistoriesById(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.AbortWithStatusJSON(400, gin.H{"code": 400, "message": "id参数错误"})
		return
	}
	server, ok := singleton.ServerList[id]
	if !ok {
		c.AbortWithStatusJSON(404, gin.H{
			"code":    404,
			"message": "id不存在",
		})
		return
	}
	c.JSON(200, singleton.MonitorAPI.GetMonitorHistories(map[string]any{"server_id": server.ID}, 4320))
}

func (v *apiV1) monitorHistoriesDayById(c *gin.Context) {
	id := c.Param("id")
	c.JSON(200, singleton.MonitorAPI.GetMonitorHistories(map[string]any{"monitor_id": id}, 24*60))
}

func (v *apiV1) monitorHistoriesMonthById(c *gin.Context) {
	id := c.Param("id")
	c.JSON(200, singleton.MonitorAPI.GetMonitorHistories(map[string]any{"monitor_id": id}, 24*60*30))
}

func (v *apiV1) monitorHistories(c *gin.Context) {
	c.JSON(200, singleton.MonitorAPI.GetMonitorHistories(nil, 0))
}
