// Copyright (c) 2026 @3899. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be found in the LICENSE file.

package ncmm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/3899/ncmm/api"
	"github.com/3899/ncmm/api/eapi"
	"github.com/3899/ncmm/api/types"
	"github.com/3899/ncmm/api/weapi"
	"github.com/3899/ncmm/pkg/log"

	"github.com/spf13/cobra"
)

type SignInOpts struct {
	Automatic bool
}

type SignIn struct {
	root *Root
	cmd  *cobra.Command
	l    *log.Logger
	opts SignInOpts
}

func NewSign(root *Root, l *log.Logger) *SignIn {
	c := &SignIn{
		root: root,
		l:    l,
		cmd: &cobra.Command{
			Use:     "sign",
			Short:   "[need login] Sign perform daily cloud shell check-in",
			Example: `  ncmm sign`,
		},
	}
	c.addFlags()
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return c.execute(cmd.Context())
	}
	return c
}

func (c *SignIn) addFlags() {
	c.cmd.Flags().BoolVarP(&c.opts.Automatic, "automatic", "a", false, "automatically claim sign-in rewards")
}

func (c *SignIn) validate() error {
	return nil
}

func (c *SignIn) isAutomatic() bool {
	if c.opts.Automatic {
		return true
	}
	if c.root.Cfg.Sign != nil && c.root.Cfg.Sign.Automatic {
		return true
	}
	return false
}

func (c *SignIn) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *SignIn) Command() *cobra.Command {
	return c.cmd
}

func (c *SignIn) execute(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	cfg := c.root.Cfg
	if cfg.Accounts == nil {
		return fmt.Errorf("配置文件中缺少 accounts 账号节点")
	}

	var hasExecuted bool

	// 1. 主账号一键签到
	if cfg.Sign != nil && cfg.Sign.EnableMain && cfg.Accounts.Main != "" {
		c.cmd.Printf("[sign] >>>>>> 开始主账号签到 (%s) <<<<<<\n", cfg.Accounts.Main)
		if err := c.runSignForCookie(ctx, cfg.Accounts.Main, true); err != nil {
			c.cmd.Printf("[sign] ❌ 主账号签到失败: %s\n", err)
		}
		hasExecuted = true
	}

	// 2. 辅助账号一键签到
	if cfg.Sign != nil && cfg.Sign.EnableSecondaries && len(cfg.Accounts.Secondary) > 0 {
		for _, secCookie := range cfg.Accounts.Secondary {
			c.cmd.Printf("[sign] >>>>>> 开始辅助账号签到 (%s) <<<<<<\n", secCookie)
			if err := c.runSignForCookie(ctx, secCookie, false); err != nil {
				c.cmd.Printf("[sign] ❌ 辅助账号签到失败: %s\n", err)
			}
			hasExecuted = true
		}
	}

	if !hasExecuted {
		c.cmd.Println("[sign] 未启用或未配置任何账号进行日常签到，请检查 config.yaml")
	} else {
		c.cmd.Println("[sign] 所有日常签到及播放任务执行完毕！")
	}
	return nil
}

func (c *SignIn) runSignForCookie(ctx context.Context, cookieFile string, isPrimary bool) error {
	absPath, err := filepath.Abs(cookieFile)
	if err != nil {
		return fmt.Errorf("解析 cookie 路径失败: %w", err)
	}

	// 复制并重构 Cookie 路径
	networkCfg := *c.root.Cfg.Network
	networkCfg.Cookie.Filepath = absPath

	cli, err := api.NewClient(&networkCfg, c.l)
	if err != nil {
		return fmt.Errorf("实例化客户端失败: %w", err)
	}
	defer cli.Close(ctx)
	request := weapi.New(cli)

	// 判断是否需要登录
	if request.NeedLogin(ctx) {
		return fmt.Errorf("Cookie 已失效，需要登录 (文件: %s)", cookieFile)
	}

	// 尝试读取个人信息友好提示
	var userId int64
	vipPoint, err := request.VipGrowPoint(ctx, &weapi.VipGrowPointReq{})
	if err == nil && vipPoint.Code == 200 {
		userId = vipPoint.Data.UserLevel.UserId
		c.cmd.Printf("  [当前账号信息] Uid: %d | 等级: %s (Lv.%d)\n", userId, vipPoint.Data.UserLevel.LevelName, vipPoint.Data.UserLevel.Level)
	}

	// 1. 音乐人签到 + 领取云豆
	c.cmd.Println("  --- 音乐人任务 ---")
	signResp, err := request.MusicianSign(ctx, &weapi.MusicianSignReq{})
	if err != nil {
		log.Warn("MusicianSign err: %s", err)
	} else if signResp.Code == 200 {
		c.cmd.Println("  ✅ 音乐人签到成功")
	} else {
		c.cmd.Printf("  提示: code=%d msg=%s\n", signResp.Code, signResp.Message)
	}

	// 获取音乐人任务列表并领取云豆
	var allTasks []weapi.MusicianTask

	// 1. 获取音乐人周期任务列表
	cycleTasks, err := request.MusicianTasks(ctx, &weapi.MusicianTasksReq{})
	if err != nil {
		c.cmd.Printf("  获取音乐人周期任务失败: %s\n", err)
	} else if cycleTasks.Code == 200 {
		allTasks = append(allTasks, cycleTasks.Data.TaskList...)
	} else {
		c.cmd.Printf("  获取音乐人周期任务提示: code=%d msg=%s\n", cycleTasks.Code, cycleTasks.Message)
	}

	// 2. 获取音乐人阶段任务列表
	stageTasks, err := request.MusicianTasksNew(ctx, &weapi.MusicianTasksNewReq{})
	if err != nil {
		c.cmd.Printf("  获取音乐人阶段任务失败: %s\n", err)
	} else if stageTasks.Code == 200 {
		allTasks = append(allTasks, stageTasks.Data.TaskList...)
	} else {
		c.cmd.Printf("  获取音乐人阶段任务提示: code=%d msg=%s\n", stageTasks.Code, stageTasks.Message)
	}

	if len(allTasks) == 0 {
		c.cmd.Println("  暂无音乐人任务")
	} else {
		var claimCount int
		for _, task := range allTasks {
			c.cmd.Printf("  任务: %s | 状态: %d | 进度: %d/%d\n",
				task.Name, task.Status, task.CurrentProgress, task.TargetWorth)
			if task.Status == 2 || (task.UserMissionId > 0 && task.CurrentProgress >= task.TargetWorth && task.TargetWorth > 0) {
				id := fmt.Sprintf("%d", task.UserMissionId)
				period := fmt.Sprintf("%d", task.Period)
				reward, err := request.MusicianCloudbeanObtain(ctx, &weapi.MusicianCloudbeanObtainReq{UserMissionId: id, Period: period})
				if err != nil {
					c.cmd.Printf("  ❌ 领取云豆失败 [%s]: %s\n", task.Name, err)
				} else if reward.Code == 200 {
					c.cmd.Printf("  ✅ 领取云豆成功 [%s] (id=%s)\n", task.Name, id)
					claimCount++
				} else {
					c.cmd.Printf("  ❌ 领取云豆失败 [%s]: code=%d msg=%s\n", task.Name, reward.Code, reward.Message)
				}
			}
		}
		if claimCount > 0 {
			c.cmd.Printf("  共完成 %d 个云豆奖励的领取\n", claimCount)
		}
	}

	// 2. 执行云贝签到
	c.cmd.Println("  --- 云贝任务 ---")
	yunbeiResp, err := request.YunBeiSignIn(ctx, &weapi.YunBeiSignInReq{})
	if err != nil {
		c.cmd.Printf("  云贝签到接口错误: %s\n", err)
	} else if yunbeiResp.Code != 200 {
		c.cmd.Printf("  云贝签到失败: %+v\n", yunbeiResp)
	} else {
		if yunbeiResp.Data.Sign {
			c.cmd.Println("  ✅ 云贝签到成功")
		} else {
			c.cmd.Println("  云贝今天已签到过")
		}
	}

	// 获取签到进度与自动领取
	if c.isAutomatic() {
		progress, err := request.YunBeiSignInProgress(ctx, &weapi.YunBeiSignInProgressReq{})
		if err == nil && progress.Code == 200 {
			for _, v := range progress.Data.LotteryConfig {
				if v.BaseLotteryId <= 0 && v.ExtraLotteryId <= 0 {
					continue
				}
				reply, err := request.YunBeiSignLottery(ctx, &weapi.YunBeiSignLotteryReq{
					UserLotteryId: fmt.Sprintf("%d", v.BaseLotteryId),
				})
				if err != nil {
					log.Error("YunBeiSignLottery(%v): %v", v.BaseLotteryId, err)
				}
				if reply.Data {
					c.cmd.Printf("  云贝连续签到 [%v天], 额外奖励 [%v] 领取成功\n", v.SignDay, v.BaseGrant.Name)
				}
			}
		}

		// 执行云贝系列自动任务打卡并领奖
		c.handleYunbeiTasks(ctx, cli, request, userId, cookieFile)
	}

	// 3. 黑胶 VIP 会员任务
	c.cmd.Println("  --- VIP 任务 ---")
	if vipPoint != nil && vipPoint.Code == 200 {
		if vipPoint.Data.UserLevel.LatestVipStatus != 1 {
			c.cmd.Printf("  暂无会员权益 (VIP 状态: %v)\n", vipPoint.Data.UserLevel.LatestVipStatus)
		} else {
			enableVipTask := true
			if c.root.Cfg.Sign != nil && c.root.Cfg.Sign.EnableVipTask != nil {
				enableVipTask = *c.root.Cfg.Sign.EnableVipTask
			}

			userLevel := vipPoint.Data.UserLevel.Level

			if enableVipTask {
				c.handleVipTasks(ctx, cli, request, userLevel)
			} else {
				// 原有的极简 WEAPI 黑胶签到与领取逻辑
				vipSign, err := request.VipTaskSign(ctx, &weapi.VipTaskSignReq{IsNew: ""})
				if err == nil && vipSign.Data {
					c.cmd.Println("  ✅ 黑胶 VIP 乐签成功")
				} else {
					c.cmd.Println("  黑胶 VIP 今天已乐签过")
				}

				if c.isAutomatic() {
					reward, err := request.VipRewardGetAll(ctx, &weapi.VipRewardGetAllReq{})
					if err == nil && reward.Data.Result {
						c.cmd.Println("  ✅ 黑胶成长值已一键领取成功")
					}
				}
			}
		}
	}

	// 4. 刷新 token 维持会话
	refresh, err := request.TokenRefresh(ctx, &weapi.TokenRefreshReq{})
	if err != nil || refresh.Code != 200 {
		log.Debug("TokenRefresh err: %s", err)
	}

	c.cmd.Printf("[sign] -----------------------------------------------\n\n")
	return nil
}

// handleYunbeiTasks 云贝任务主处理器，处理打卡和自动领奖
func (c *SignIn) handleYunbeiTasks(ctx context.Context, cli *api.Client, request *weapi.Api, userId int64, cookieFile string) {
	eapiRequest := eapi.New(cli)
	// 1. 获取当前待做任务列表 (作为执行云贝签到任务的前置动作)
	task, err := eapiRequest.YunBeiTaskTodo(ctx, &eapi.YunBeiTaskTodoReq{})
	if err != nil || task.Code != 200 {
		c.cmd.Printf("  ❌ 获取云贝任务列表失败: %v\n", err)
		return
	}

	c.cmd.Println("  👉 成功获取云贝任务列表:")
	for _, v := range task.Data {
		statusStr := "已完成"
		if !v.Completed {
			statusStr = "未完成"
		}
		c.cmd.Printf("    - 任务: %-15s | 状态: %-6s | 奖励: %d 云贝\n", v.TaskName, statusStr, v.TaskPoint)
	}

	// 2. 预约领云贝 (特殊板块)
	c.handleReserveYunbei(ctx, eapiRequest)

	// 3. 筛选并执行未完成的任务
	var playDailyRecommendTaskName string
	for _, v := range task.Data {
		if v.Completed {
			continue
		}

		switch v.TaskName {
		case "浏览会员中心":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableViewVipCenter {
				c.cmd.Println("  👉 开始执行 [浏览会员中心] 任务...")
				c.doViewVipCenter(ctx, eapiRequest)
			}
		case "点赞评论、动态", "点赞":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableLikeComment {
				c.cmd.Printf("  👉 开始执行 [%s] 任务 (使用 [点赞评论] 开关控制)...\n", v.TaskName)
				c.doLikeComments(ctx, request)
			}
		case "探索小众歌曲":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableListenIndie {
				c.cmd.Println("  👉 开始执行 [探索小众歌曲] 听歌任务...")
				c.doListenIndie(ctx, eapiRequest, request)
			}
		case "关注歌手":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableFollowArtist {
				c.cmd.Println("  👉 开始执行 [关注歌手] 任务...")
				c.doFollowArtist(ctx, eapiRequest)
			}
		case "收藏":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableCollectSong {
				c.cmd.Println("  👉 开始执行 [收藏] 任务 (使用 [收藏歌曲] 开关控制)...")
				c.doCollectSong(ctx, request, userId)
			}
		case "红心歌曲", "红心":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnableLikeSong {
				c.cmd.Printf("  👉 开始执行 [%s] 任务 (使用 [红心歌曲] 开关控制)...\n", v.TaskName)
				c.doLikeSong(ctx, eapiRequest)
			}
		case "发布动态", "分享动态", "发布图文", "发布图文动态", "发布笔记", "分享图文", "发布图文笔记":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnablePublishNote {
				c.cmd.Printf("  👉 开始执行 [%s] 任务 (使用 [发布动态] 开关控制)...\n", v.TaskName)
				c.doPublishNote(ctx, cookieFile)
			}
		case "听歌30分钟", "听歌", "每日推荐", "听推荐歌曲", "听推荐歌单中的歌", "听音乐30分钟":
			if c.root.Cfg.Sign.YunbeiTask != nil && c.root.Cfg.Sign.YunbeiTask.EnablePlayDailyRecommend {
				playDailyRecommendTaskName = v.TaskName
			}
		}
	}

	// 4. 执行日推播放任务 (前台串行，作为当前账号最后一个任务执行)
	if playDailyRecommendTaskName != "" {
		c.cmd.Printf("  👉 开始执行 [%s] 任务 (使用 [播放日推] 开关控制)...\n", playDailyRecommendTaskName)
		c.doPlayDailyRecommend(ctx, cookieFile, playDailyRecommendTaskName)
	}

	// 5. 重新获取任务列表以获取最新的 userTaskId 和 depositCode 以供完成领奖
	c.cmd.Println("  👉 重新获取任务列表并领取奖励...")
	refreshedTask, err := eapiRequest.YunBeiTaskTodo(ctx, &eapi.YunBeiTaskTodoReq{})
	if err == nil && refreshedTask.Code == 200 {
		var claimedCount int
		for _, v := range refreshedTask.Data {
			if !v.Completed {
				continue
			}
			reply, err := request.YunBeiTaskFinish(ctx, &weapi.YunBeiTaskFinishReq{
				Period:      fmt.Sprintf("%d", v.Period),
				UserTaskId:  fmt.Sprintf("%d", v.UserTaskId),
				DepositCode: fmt.Sprintf("%d", v.DepositCode),
			})
			if err == nil && reply.Code == 200 {
				c.cmd.Printf("  🎉 成功领取云贝 [%s] 任务奖励，获得云贝: %v\n", v.TaskName, v.TaskPoint)
				claimedCount++
			}
		}
		if claimedCount == 0 {
			c.cmd.Println("  ℹ️ 没有可领取的任务奖励")
		}

		// 6. 领奖后再次获取任务列表并打印最终状态
		finalTask, err := eapiRequest.YunBeiTaskTodo(ctx, &eapi.YunBeiTaskTodoReq{})
		if err == nil && finalTask.Code == 200 {
			c.cmd.Println("  👉 领奖后最终的任务列表:")
			for _, v := range finalTask.Data {
				statusStr := "已完成"
				if !v.Completed {
					statusStr = "未完成"
				}
				c.cmd.Printf("    - 任务: %-15s | 状态: %-6s | 奖励: %d 云贝\n", v.TaskName, statusStr, v.TaskPoint)
			}
		} else {
			c.cmd.Printf("  ❌ 领奖后获取任务列表失败: %v\n", err)
		}
	} else {
		c.cmd.Printf("  ❌ 重新拉取任务列表失败: %v\n", err)
	}
}

// doViewVipCenter 执行浏览会员中心任务（15~25秒随机延迟）
func (c *SignIn) doViewVipCenter(ctx context.Context, eapiRequest *eapi.Api) {
	_, err := eapiRequest.YunbeiClickTask(ctx, &eapi.YunbeiClickTaskReq{
		TaskId:     6758460,
		SubAction:  "weibo",
		Type:       "feizhu",
		CheckToken: "",
	})
	if err != nil {
		c.cmd.Printf("  ❌ 浏览会员中心触发失败: %v\n", err)
		return
	}
	sleepSec := 15 + rand.Intn(11) // 15 ~ 25 秒随机
	c.cmd.Printf("  ⏳ 已模拟触发浏览会员中心任务，随机浏览 %d 秒...\n", sleepSec)
	time.Sleep(time.Duration(sleepSec) * time.Second)
	c.cmd.Println("  ✅ 浏览会员中心模拟结束")
}

// doLikeComments 点赞热门评论（点赞10个，3~10秒随机延迟后取消点赞）
func (c *SignIn) doLikeComments(ctx context.Context, request *weapi.Api) {
	comments, err := request.Comments(ctx, &weapi.CommentsReq{
		ThreadId: "R_SO_4_186016", // 晴天的ThreadId
		Limit:    "20",
		Offset:   "0",
	})
	if err != nil || comments.Code != 200 || len(comments.Comments) == 0 {
		c.cmd.Printf("  ❌ 获取用于点赞的评论列表失败: %v\n", err)
		return
	}

	targetCount := 10
	if len(comments.Comments) < targetCount {
		targetCount = len(comments.Comments)
	}

	c.cmd.Printf("  👉 准备点赞并取消点赞 %d 条评论...\n", targetCount)
	var successCount int
	for i := 0; i < targetCount; i++ {
		comment := comments.Comments[i]
		commentIdStr := fmt.Sprintf("%d", comment.CommentId)

		// 1. 点赞
		_, err := request.CommentLike(ctx, &weapi.CommentLikeReq{
			ThreadId:  "R_SO_4_186016",
			CommentId: commentIdStr,
		})
		if err == nil {
			// 2. 延迟 3~10 秒后取消点赞
			sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
			time.Sleep(time.Duration(sleepSec) * time.Second)
			_, _ = request.CommentUnlike(ctx, &weapi.CommentLikeReq{
				ThreadId:  "R_SO_4_186016",
				CommentId: commentIdStr,
			})
			successCount++
		}
	}
	c.cmd.Printf("  ✅ 点赞评论操作完成，成功: %d/%d\n", successCount, targetCount)
}

// doListenIndie 探索小众歌曲听歌（严格串行，每首交替上报凭证和听歌状态）
func (c *SignIn) doListenIndie(ctx context.Context, eapiRequest *eapi.Api, request *weapi.Api) {
	recommend, err := eapiRequest.YunbeiDistributionRecommendSong(ctx, &eapi.YunbeiDistributionRecommendSongReq{
		Offset: 0,
		Limit:  10,
	})
	if err != nil || recommend.Code != 200 || len(recommend.Data) == 0 {
		c.cmd.Printf("  ❌ 获取小众推荐歌曲失败: %v\n", err)
		return
	}

	targetCount := 10
	if len(recommend.Data) < targetCount {
		targetCount = len(recommend.Data)
	}

	// 批量查询歌曲详情以获得正确的 AlbumId 和歌名
	songIds := make([]string, targetCount)
	for i := 0; i < targetCount; i++ {
		songIds[i] = fmt.Sprintf("%d", recommend.Data[i].SongId)
	}
	reqList := make([]weapi.SongDetailReqList, len(songIds))
	for i, id := range songIds {
		reqList[i] = weapi.SongDetailReqList{Id: id, V: 0}
	}
	albumMap := make(map[int64]int64)
	nameMap := make(map[int64]string)
	details, detailErr := request.SongDetail(ctx, &weapi.SongDetailReq{C: reqList})
	if detailErr == nil && details != nil {
		for _, s := range details.Songs {
			albumMap[s.Id] = s.Al.Id
			nameMap[s.Id] = s.Name
		}
	}

	c.cmd.Printf("  👉 已拉取到 %d 首推荐小众歌曲，开始依次串行听歌打卡...\n", targetCount)

	for i := 0; i < targetCount; i++ {
		song := recommend.Data[i]
		sId := song.SongId
		aId := albumMap[sId]
		if aId == 0 {
			aId = song.AlbumId
		}
		songName := nameMap[sId]
		if songName == "" {
			songName = "未知歌曲"
		}

		// 31~50秒之间随机延迟听歌
		sleepSec := 31 + rand.Intn(20)
		c.cmd.Printf("  ⏳ 正在听第 %d/%d 首小众歌曲: ID=%d , 歌名=%s (模拟播放 %d秒)...\n", i+1, targetCount, sId, songName, sleepSec)

		// 阻塞等待模拟播放完成
		select {
		case <-ctx.Done():
			c.cmd.Println("  ❌ 听歌打卡被取消")
			return
		case <-time.After(time.Duration(sleepSec) * time.Second):
		}

		// 1. 请求云贝分配创建凭证 (YunbeiDistributionCreate)
		dist, distErr := eapiRequest.YunbeiDistributionCreate(ctx, &eapi.YunbeiDistributionCreateReq{
			YunbeiAmount: 150,
		})
		if distErr != nil {
			c.cmd.Printf("  ❌ [听歌打卡 %d/%d] 申请云贝分配凭证失败: %v\n", i+1, targetCount, distErr)
		} else if dist.Code == 200 && dist.Data {
			c.cmd.Printf("  🎉 [听歌打卡 %d/%d] 成功申请云贝分配凭证\n", i+1, targetCount)
		} else {
			c.cmd.Printf("  ⚠️ [听歌打卡 %d/%d] 申请云贝分配凭证异常: code=%d msg=%s\n", i+1, targetCount, dist.Code, dist.Message)
		}

		// 2. 额外延迟 2~5 秒以模拟前台点击或缓冲
		extraSleep := 2 + rand.Intn(4)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(extraSleep) * time.Second):
		}

		// 3. 上报听歌打卡 (TrialsongListen)
		_, listenErr := eapiRequest.TrialsongListen(ctx, &eapi.TrialsongListenReq{
			SongId:  fmt.Sprintf("%d", sId),
			AlbumId: fmt.Sprintf("%d", aId),
			Scene:   1,
		})
		if listenErr != nil {
			c.cmd.Printf("  ❌ [听歌打卡 %d/%d] (歌曲 ID: %d) 上报失败: %v\n", i+1, targetCount, sId, listenErr)
		} else {
			c.cmd.Printf("  ✅ [听歌打卡 %d/%d] (歌曲 ID: %d) 听歌完毕，成功上报\n", i+1, targetCount, sId)
		}

		// 如果不是最后一首歌曲，切歌过渡时睡眠 1~3 秒
		if i < targetCount-1 {
			transitionSleep := 1 + rand.Intn(3)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(transitionSleep) * time.Second):
			}
		}
	}
	c.cmd.Println("  ✅ 探索小众歌曲 10 首打卡完毕")
}

// handleReserveYunbei 活动预约领云贝（自动预约、领奖）
func (c *SignIn) handleReserveYunbei(ctx context.Context, eapiRequest *eapi.Api) {
	if c.root.Cfg.Sign.YunbeiTask == nil || !c.root.Cfg.Sign.YunbeiTask.EnableReserve {
		return
	}

	c.cmd.Println("  👉 正在检查 [预约领云贝] 状态...")
	info, err := eapiRequest.YunbeiReserveInfo(ctx, &eapi.YunbeiReserveInfoReq{})
	if err != nil || info.Code != 200 {
		c.cmd.Printf("  ❌ 获取活动预约状态失败: %v\n", err)
		return
	}

	chineseStatus := getReserveStatusChinese(info.Data.Type)

	// 检测到未预约，去预约 (包含任何 NO_BOOK 子串状态, 例如 PREV_CLAIMED_NO_BOOKED, NO_PREV_NO_BOOK 等)
	if strings.Contains(info.Data.Type, "NO_BOOK") {
		c.cmd.Printf("  👉 检测到当前预约状态: [%s]，开始执行预约...\n", chineseStatus)
		booked, err := eapiRequest.YunbeiReserveBooked(ctx, &eapi.YunbeiReserveBookedReq{ReqId: info.Data.ReqId})
		if err == nil && booked.Code == 200 {
			c.cmd.Println("  ✅ 预约活动成功！")
		} else {
			c.cmd.Printf("  ❌ 预约活动失败: %v\n", err)
		}
		return
	}

	// 检测到已预约但未领，执行领取
	if info.Data.Type == "PREV_BOOKED_UNCLAIMED" {
		c.cmd.Printf("  👉 检测到当前预约状态: [%s]，开始领取云贝...\n", chineseStatus)
		receive, err := eapiRequest.YunbeiReserveRewardReceive(ctx, &eapi.YunbeiReserveRewardReceiveReq{
			ReqId:      info.Data.ReqId,
			CheckToken: "",
		})
		if err == nil && receive.Code == 200 {
			c.cmd.Printf("  🎉 预约活动奖励领取成功，获得云贝数量：%v\n", receive.Data.CurrentAmount)

			// 领取成功后，重新获取最新的预约信息并执行自动预约新活动
			c.cmd.Println("  👉 奖励领取成功，正在检查并自动预约新活动...")
			info, err = eapiRequest.YunbeiReserveInfo(ctx, &eapi.YunbeiReserveInfoReq{})
			if err == nil && info.Code == 200 {
				chineseStatus = getReserveStatusChinese(info.Data.Type)
				if strings.Contains(info.Data.Type, "NO_BOOK") {
					c.cmd.Printf("  👉 检测到当前预约状态: [%s]，开始执行预约...\n", chineseStatus)
					booked, err := eapiRequest.YunbeiReserveBooked(ctx, &eapi.YunbeiReserveBookedReq{ReqId: info.Data.ReqId})
					if err == nil && booked.Code == 200 {
						c.cmd.Println("  ✅ 预约活动成功！")
					} else {
						c.cmd.Printf("  ❌ 预约活动失败: %v\n", err)
					}
				} else {
					c.cmd.Printf("  ℹ️ 当前预约状态: [%s]，无须做预约操作\n", chineseStatus)
				}
			} else {
				c.cmd.Printf("  ❌ 重新获取活动预约状态失败: %v\n", err)
			}
		} else {
			c.cmd.Printf("  ❌ 领取预约活动奖励失败: %v\n", err)
		}
		return
	}

	c.cmd.Printf("  ℹ️ 当前预约状态: [%s]，无须做预约或领奖操作\n", chineseStatus)
}

// doFollowArtist 关注歌手（获取热门，关注第一个，3~10秒随机延迟后取消关注）
func (c *SignIn) doFollowArtist(ctx context.Context, eapiRequest *eapi.Api) {
	var artistIdStr = "6452" // 默认周杰伦
	var artistName = "周杰伦"

	hot, err := eapiRequest.ArtistHot(ctx, &eapi.ArtistHotReq{Offset: 0, Limit: 10})
	if err == nil && hot.Code == 200 && len(hot.Data) > 0 && len(hot.Data[0].Artists) > 0 {
		found := false
		for _, artist := range hot.Data[0].Artists {
			// 必须找一个目前未关注过的歌手，否则关注已关注的歌手不会触发任务完成
			if !artist.Followed {
				artistIdStr = fmt.Sprintf("%d", artist.Id)
				artistName = artist.Name
				found = true
				break
			}
		}
		if !found {
			// 如果全都是已关注的，只能默认选第一个
			artist := hot.Data[0].Artists[0]
			artistIdStr = fmt.Sprintf("%d", artist.Id)
			artistName = artist.Name
		}
	} else {
		c.cmd.Printf("  ℹ️ 获取热门歌手失败 (err=%v)，将使用默认歌手 [周杰伦] 进行关注歌手任务...\n", err)
	}

	// 1. 关注
	subResp, subErr := eapiRequest.ArtistSub(ctx, &eapi.ArtistSubReq{
		ArtistId: artistIdStr,
	})
	if subErr != nil {
		c.cmd.Printf("  ❌ 关注歌手 [%s] 失败: %v\n", artistName, subErr)
		return
	}
	if subResp.Code != 200 {
		c.cmd.Printf("  ❌ 关注歌手 [%s] 失败: Code=%d, Message=%s\n", artistName, subResp.Code, subResp.Message)
		return
	}

	// 2. 延迟 3~10 秒取消关注
	sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
	c.cmd.Printf("  ⏳ 已成功关注歌手 [%s]，将在 %d 秒后取消关注以避嫌...\n", artistName, sleepSec)
	time.Sleep(time.Duration(sleepSec) * time.Second)

	unsubResp, unsubErr := eapiRequest.ArtistUnsub(ctx, &eapi.ArtistUnsubReq{
		ArtistIds: fmt.Sprintf("[%s]", artistIdStr),
	})
	if unsubErr != nil {
		c.cmd.Printf("  ❌ 取消关注歌手 [%s] 失败: %v\n", artistName, unsubErr)
		return
	}
	if unsubResp.Code != 200 {
		c.cmd.Printf("  ❌ 取消关注歌手 [%s] 失败: Code=%d, Message=%s\n", artistName, unsubResp.Code, unsubResp.Message)
		return
	}
	c.cmd.Printf("  ✅ 关注并取消关注歌手 [%s] 操作完毕\n", artistName)
}

// doLikeSong 红心歌曲（获取日推，红心第一个，3~10秒随机延迟后取消红心）
func (c *SignIn) doLikeSong(ctx context.Context, eapiRequest *eapi.Api) {
	var trackIdStr = "186016" // 默认晴天
	var songName = "晴天"

	songs, err := eapiRequest.DiscoveryRecommendSongs(ctx, &eapi.DiscoveryRecommendSongsReq{})
	if err == nil && songs.Code == 200 && len(songs.Data.DailySongs) > 0 {
		song := songs.Data.DailySongs[0]
		trackIdStr = fmt.Sprintf("%d", song.Id)
		songName = song.Name
	} else {
		c.cmd.Println("  ℹ️ 获取日推歌曲失败，将使用默认歌曲 [晴天] 进行红心歌曲任务...")
	}

	// 1. 红心歌曲
	_, likeErr := eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
		TrackId:    trackIdStr,
		Like:       "true",
		Time:       "3",
		CheckToken: "",
	})
	if likeErr != nil {
		c.cmd.Printf("  ❌ 红心歌曲《%s》失败: %v\n", songName, likeErr)
		return
	}

	// 2. 延迟 3~10 秒取消红心
	sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
	c.cmd.Printf("  ⏳ 已红心歌曲《%s》，将在 %d 秒后取消红心以避嫌...\n", songName, sleepSec)
	time.Sleep(time.Duration(sleepSec) * time.Second)

	_, _ = eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
		TrackId:    trackIdStr,
		Like:       "false",
		Time:       "3",
		CheckToken: "",
	})
	c.cmd.Printf("  ✅ 红心并取消红心歌曲《%s》操作完毕\n", songName)
}

// getReserveStatusChinese 将英文预约状态转换为易读的中文
func getReserveStatusChinese(status string) string {
	switch status {
	case "NO_PREV_NO_BOOK":
		return "未领奖且未预约"
	case "PREV_CLAIMED_NO_BOOKED":
		return "上次奖励已领，新活动待预约"
	case "PREV_BOOKED_UNCLAIMED":
		return "已预约，有待领奖励"
	case "PREV_CLAIMED_BOOKED":
		return "上次奖励已领，新活动已预约"
	default:
		if strings.Contains(status, "BOOKED") || strings.Contains(status, "BOOK") {
			if strings.Contains(status, "NO_BOOK") || strings.Contains(status, "NO_BOOKED") {
				return "新活动待预约"
			}
			return "已预约活动"
		}
		return status
	}
}

// doCollectSong 收藏一首歌曲（获取用户歌单，添加第一首歌，3~10秒随机延迟后删除以避嫌）
func (c *SignIn) doCollectSong(ctx context.Context, request *weapi.Api, userId int64) {
	if userId == 0 {
		c.cmd.Println("  ❌ 收藏歌曲失败: 获取当前账号 Uid 失败")
		return
	}

	playlists, err := request.Playlist(ctx, &weapi.PlaylistReq{
		Uid:   fmt.Sprintf("%d", userId),
		Limit: "5",
	})
	if err != nil || playlists.Code != 200 || len(playlists.Playlist) == 0 {
		c.cmd.Printf("  ❌ 获取歌单列表失败: %v\n", err)
		return
	}

	pid := playlists.Playlist[0].Id
	trackId := int64(186016) // 默认晴天

	// 1. 添加歌曲到歌单 (收藏)
	_, addErr := request.PlaylistAddOrDel(ctx, &weapi.PlaylistAddOrDelReq{
		Op:       "add",
		Pid:      pid,
		TrackIds: types.IntsString{trackId},
		Imme:     true,
	})
	if addErr != nil {
		c.cmd.Printf("  ❌ 收藏歌曲失败: %v\n", addErr)
		return
	}

	// 2. 延迟 3~10 秒后从歌单中删除该歌曲 (取消收藏)
	sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
	c.cmd.Printf("  ⏳ 已成功收藏歌曲，将在 %d 秒后从歌单 [%s] 中移除该歌曲...\n", sleepSec, playlists.Playlist[0].Name)
	time.Sleep(time.Duration(sleepSec) * time.Second)

	_, _ = request.PlaylistAddOrDel(ctx, &weapi.PlaylistAddOrDelReq{
		Op:       "del",
		Pid:      pid,
		TrackIds: types.IntsString{trackId},
		Imme:     true,
	})
	c.cmd.Println("  ✅ 收藏并取消收藏歌曲操作完毕")
}

// doPublishNote 调用 Note 服务完成图文笔记发布
func (c *SignIn) doPublishNote(ctx context.Context, cookieFile string) {
	n := NewNote(c.root, c.l)
	_, err := n.ExecuteForCookie(ctx, cookieFile)
	if err != nil {
		c.cmd.Printf("  ❌ 发布图文动态失败: %v\n", err)
	} else {
		c.cmd.Println("  ✅ 发布图文动态成功")
	}
}

// doPlayDailyRecommend 串行执行日推歌曲播放 31~45 分钟
func (c *SignIn) doPlayDailyRecommend(ctx context.Context, cookieFile string, taskName string) {
	c.cmd.Printf("  ⏳ [日推播放] 正在初始化播放服务 (%s)...\n", cookieFile)
	absPath, err := filepath.Abs(cookieFile)
	if err != nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 解析 cookie 路径失败: %v\n", cookieFile, err)
		return
	}

	networkCfg := *c.root.Cfg.Network
	networkCfg.Cookie.Filepath = absPath

	cli, err := api.NewClient(&networkCfg, c.l)
	if err != nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 实例化客户端失败: %v\n", cookieFile, err)
		return
	}
	defer cli.Close(ctx)
	request := weapi.New(cli)

	// 获取用户信息以过滤用户本人的歌
	user, err := request.GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 验证登录状态失败: %v\n", cookieFile, err)
		return
	}
	if user.Code != 200 || user.Profile == nil || user.Account == nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 用户未登录或登录态已失效\n", cookieFile)
		return
	}
	nickname := user.Profile.Nickname
	userId := user.Account.Id

	// 拉取每日推荐歌曲
	recommendResp, err := request.RecommendSongs(ctx, &weapi.RecommendSongsReq{})
	if err != nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 拉取每日推荐失败: %v\n", cookieFile, err)
		return
	}
	if recommendResp.Code != 200 || len(recommendResp.Data.DailySongs) == 0 {
		c.cmd.Printf("  ❌ [日推播放] (%s) 接口返回暂无推荐歌曲\n", cookieFile)
		return
	}

	// 随机选择 31~45 分钟听歌
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	targetMinutes := 31 + r.Int63n(15) // 31 ~ 45
	targetSeconds := targetMinutes * 60

	var recommendSongIds []string
	var totalDurationSec int64
	var targetSongsCount int64

	for _, song := range recommendResp.Data.DailySongs {
		// 过滤掉本人歌曲以避嫌
		isSelf := false
		for _, ar := range song.Ar {
			if ar.Name == nickname {
				isSelf = true
				break
			}
		}
		if song.DjId > 0 && song.DjId == userId {
			isSelf = true
		}
		if isSelf {
			c.cmd.Printf("  ⚠️ [日推播放] (%s) 检测到推荐歌曲 %s (ID: %d) 是歌手本人歌曲，自动跳过\n", cookieFile, song.Name, song.Id)
			continue
		}

		songIdStr := fmt.Sprintf("%d", song.Id)
		recommendSongIds = append(recommendSongIds, songIdStr)

		songTime := song.Dt / 1000
		if songTime <= 0 {
			songTime = 180 // default 3 minutes
		}
		// 累加歌曲时长，并加上预计间隔（例如15秒）
		totalDurationSec += songTime + 15
		targetSongsCount++

		if totalDurationSec >= targetSeconds {
			break
		}
	}

	if len(recommendSongIds) == 0 {
		c.cmd.Printf("  ❌ [日推播放] (%s) 没有可播放的推荐歌曲\n", cookieFile)
		return
	}

	c.cmd.Printf("  👉 [日推播放] (%s) 已计算完成：目标播放时间为 %d 分钟，需要播放约 %d 首日推歌。\n", cookieFile, targetMinutes, targetSongsCount)

	// 实例化 PlayIds 并执行
	p := NewPlayIds(c.root, c.l)
	p.opts = PlayIdsOpts{
		RunMin:         targetSongsCount,
		RunMax:         targetSongsCount,
		GapMin:         10, // 与默认值保持一致
		GapMax:         30,
		CookieFile:     cookieFile,
		DisableMixPlay: true, // 已经是日推歌曲了，关闭多余的混听
	}

	_, err = p.executeForCookie(ctx, cookieFile, recommendSongIds)
	if err != nil {
		c.cmd.Printf("  ❌ [日推播放] (%s) 播放日推失败: %v\n", cookieFile, err)
	} else {
		c.cmd.Printf("  ✅ [日推播放] (%s) 成功播放日推歌曲完成\n", cookieFile)
	}
}

// getDeviceId 获取本地缓存的唯一设备 ID
func getDeviceId(cli *api.Client) string {
	var deviceId string
	if ck, ok := cli.Cookie("https://music.163.com", "deviceId"); ok && ck.Value != "" {
		deviceId = ck.Value
	} else if ck, ok := cli.Cookie("https://interface3.music.163.com", "deviceId"); ok && ck.Value != "" {
		deviceId = ck.Value
	} else if ck, ok := cli.Cookie("https://music.163.com", "sDeviceId"); ok && ck.Value != "" {
		deviceId = ck.Value
	} else if ck, ok := cli.Cookie("https://interface3.music.163.com", "sDeviceId"); ok && ck.Value != "" {
		deviceId = ck.Value
	}
	return deviceId
}

type vipSignState struct {
	Today    bool
	Signed   bool
	RecordId int64
	Time     int64
	TimeStr  string
	Score    int64
}

type vipGrowthState struct {
	TodayScore          int64
	MonthTaskTotalScore int64
	CurrentDay          string
	GrowthPoint         int64
}

type vipMonthPrizeState struct {
	MonthCheckInTotalDay int64
	TodayDailyGrowth     int64
}

func newVipTaskListReq(deviceId string) *eapi.VipTaskListReq {
	return &eapi.VipTaskListReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		IsNew:    1,
		ER:       true,
	}
}

func newVipSignInfoReq(deviceId string) *eapi.VipSignInfoReq {
	return &eapi.VipSignInfoReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func newVipGrowPointReq(deviceId string) *eapi.VipGrowPointReq {
	return &eapi.VipGrowPointReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func newVipCommonReq(deviceId string) *eapi.VipCommonReq {
	return &eapi.VipCommonReq{
		DeviceId: deviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
		ER:       true,
	}
}

func isVipSignTask(task eapi.VipTaskListData) bool {
	return task.MissionId == -500 ||
		strings.Contains(task.MainTitle, "乐签") ||
		(strings.Contains(task.MainTitle, "黑胶") && strings.Contains(task.MainTitle, "签"))
}

func isVipSignTaskDone(task eapi.VipTaskListData) bool {
	return task.Status == 100 ||
		strings.Contains(task.ButtonText, "已打卡") ||
		strings.Contains(task.ButtonText, "已完成")
}

func getVipSignState(ctx context.Context, request *eapi.Api, deviceId string) (vipSignState, error) {
	reply, err := request.VipSignInfo(ctx, newVipSignInfoReq(deviceId))
	if err != nil {
		return vipSignState{}, err
	}
	if reply.Code != 200 {
		return vipSignState{}, fmt.Errorf("code=%d msg=%s", reply.Code, reply.Message)
	}
	for _, info := range reply.Data {
		if !info.Today {
			continue
		}
		return vipSignState{
			Today:    true,
			Signed:   info.RecordId > 0 || info.Time > 0,
			RecordId: info.RecordId,
			Time:     info.Time,
			TimeStr:  info.TimeStr,
			Score:    info.Score,
		}, nil
	}
	return vipSignState{}, nil
}

func getVipGrowthState(ctx context.Context, request *eapi.Api, deviceId string) (vipGrowthState, error) {
	reply, err := request.VipGrowPoint(ctx, newVipGrowPointReq(deviceId))
	if err != nil {
		return vipGrowthState{}, err
	}
	if reply.Code != 200 {
		return vipGrowthState{}, fmt.Errorf("code=%d msg=%s", reply.Code, reply.Message)
	}

	state := vipGrowthState{GrowthPoint: reply.Data.UserLevel.GrowthPoint}
	if reply.Data.UserLevel.ExtJson == "" {
		return state, nil
	}

	var ext struct {
		TodayScore          int64  `json:"todayScore"`
		MonthTaskTotalScore int64  `json:"monthTaskTotalScore"`
		CurrentDay          string `json:"currentDay"`
	}
	if err := json.Unmarshal([]byte(reply.Data.UserLevel.ExtJson), &ext); err != nil {
		return state, nil
	}
	state.TodayScore = ext.TodayScore
	state.MonthTaskTotalScore = ext.MonthTaskTotalScore
	state.CurrentDay = ext.CurrentDay
	return state, nil
}

func getVipMonthPrizeState(ctx context.Context, request *eapi.Api, deviceId string) (vipMonthPrizeState, error) {
	reply, err := request.VipMonthPrizeList(ctx, newVipCommonReq(deviceId))
	if err != nil {
		return vipMonthPrizeState{}, err
	}
	if reply.Code != 200 {
		return vipMonthPrizeState{}, fmt.Errorf("code=%d msg=%s", reply.Code, reply.Message)
	}
	return vipMonthPrizeState{
		MonthCheckInTotalDay: reply.Data.MonthCheckInTotalDay,
		TodayDailyGrowth:     reply.Data.TodayDailyGrowth,
	}, nil
}

// getVipSignCompleted 综合判定乐签是否已完成。
// taskListData 为已获取的任务列表，传 nil 时内部自行获取（避免重复请求）。
// 优先级：月签进度（更准确） > sign/info 记录（兜底）。
func getVipSignCompleted(ctx context.Context, request *eapi.Api, deviceId string, taskListData []eapi.VipTaskListData) (bool, string) {
	// 1. 主判定：任务列表 + 月签进度
	var taskDone bool
	if taskListData != nil {
		for _, task := range taskListData {
			if !isVipSignTask(task) {
				continue
			}
			taskDone = isVipSignTaskDone(task)
			break
		}
	} else {
		taskList, taskErr := request.VipTaskList(ctx, newVipTaskListReq(deviceId))
		if taskErr == nil && taskList != nil {
			for _, task := range taskList.Data {
				if !isVipSignTask(task) {
					continue
				}
				taskDone = isVipSignTaskDone(task)
				break
			}
		}
	}
	if taskDone {
		month, monthErr := getVipMonthPrizeState(ctx, request, deviceId)
		if monthErr == nil && month.TodayDailyGrowth > 0 && month.MonthCheckInTotalDay > 0 {
			return true, fmt.Sprintf("任务中心已打卡，月签进度=%d天，今日成长值+%d", month.MonthCheckInTotalDay, month.TodayDailyGrowth)
		}
	}

	// 2. 兜底：sign/info 记录
	if state, err := getVipSignState(ctx, request, deviceId); err == nil && state.Signed {
		return true, fmt.Sprintf("乐签记录已确认，今日成长值+%d", state.Score)
	}

	return false, ""
}

func (c *SignIn) executeVipSign(ctx context.Context, request *eapi.Api, deviceId string) bool {
	c.cmd.Println("  👉 调用黑胶乐签接口...")
	resp, err := request.VipTaskSign(ctx, &eapi.VipTaskSignReq{
		Header: struct{}{},
		IsNew:  "1",
		ER:     true,
	})
	if err != nil {
		c.cmd.Printf("  ⚠️ 黑胶乐签接口请求失败: %v\n", err)
		return false
	}
	c.cmd.Printf("  👉 黑胶乐签接口返回: code=%d data=%v msg=%s\n", resp.Code, resp.Data, resp.Message)
	if resp.Code != 200 {
		return false
	}

	// 接口返回 200 后，等待 2 秒让服务端完成异步写入，再做一次综合验证
	c.cmd.Println("  👉 打卡请求已被服务端接收，等待服务端异步处理...")
	select {
	case <-ctx.Done():
		return false
	case <-time.After(2 * time.Second):
	}
	if ok, reason := getVipSignCompleted(ctx, request, deviceId, nil); ok {
		c.cmd.Printf("  ✅ 乐签验证通过: %s\n", reason)
		return true
	}
	// 验证未通过但接口已返回成功，仍视为成功（服务端异步延迟）
	c.cmd.Println("  ✅ 乐签接口已返回成功 (code=200)，服务端状态可能存在异步延迟")
	return true
}

func (c *SignIn) handleVipTasks(ctx context.Context, cli *api.Client, request *weapi.Api, userLevel int64) {
	eapiRequest := eapi.New(cli)
	var likedSongIds []string

	deviceId := getDeviceId(cli)

	if deviceId != "" {
		c.cmd.Printf("  👉 [deviceId] 使用本地已缓存的唯一设备 ID: %s\n", deviceId)
	} else {
		c.cmd.Println("  👉 [deviceId] 缓存中无有效设备 ID，本次打卡将不传递设备 ID 字段以规避验证风险。")
	}

	// 0. 获取今日乐签状态作为真实现状。
	var signedToday bool
	beforeTodayScore := int64(-1)
	if growth, err := getVipGrowthState(ctx, eapiRequest, deviceId); err == nil {
		beforeTodayScore = growth.TodayScore
		c.cmd.Printf("  👉 [执行前] 黑胶成长值现状: 今日已获得 %d，当前成长值 %d\n", growth.TodayScore, growth.GrowthPoint)
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶成长值现状失败: %v\n", err)
	}
	// 1. 获取任务列表 (前置对照)
	c.cmd.Println("  👉 获取黑胶 VIP 任务列表...")
	taskList, err := eapiRequest.VipTaskList(ctx, newVipTaskListReq(deviceId))
	if err != nil || taskList.Code != 200 {
		c.cmd.Printf("  ❌ 获取黑胶 VIP 任务列表失败: %v\n", err)
		return
	}
	var signVerifyReason string
	if !signedToday {
		if ok, reason := getVipSignCompleted(ctx, eapiRequest, deviceId, taskList.Data); ok {
			signedToday = true
			signVerifyReason = reason
		}
	}

	c.cmd.Println("  👉 黑胶 VIP 任务列表 (执行前):")
	for _, v := range taskList.Data {
		statusStr := "未完成"
		if v.Status == 100 {
			statusStr = "已完成"
		}
		if isVipSignTask(v) {
			if signedToday {
				statusStr = "已完成"
			} else {
				statusStr = "未完成"
			}
		}
		worth := v.Worth
		if worth == 0 && isVipSignTask(v) {
			worth = 3
		}
		c.cmd.Printf("    - 任务: %-15s | 状态: %-6s | 奖励: %d成长值\n", v.MainTitle, statusStr, worth)
	}
	if signVerifyReason != "" {
		c.cmd.Printf("  ✅ 乐签状态验证通过: %s\n", signVerifyReason)
	}

	// 2. 自动做任务
	var hasVipSignTask bool
	for _, v := range taskList.Data {
		// 强行前置拦截打卡任务 (名称模糊匹配以防服务端文案变化)
		if isVipSignTask(v) {
			hasVipSignTask = true
			if signedToday {
				c.cmd.Println("  ℹ️ 黑胶乐签打卡今日已完成 (跳过)")
				continue
			}
			if isVipSignTaskDone(v) {
				c.cmd.Println("  ⚠️ 任务中心声称黑胶乐签已打卡，但 sign/info 未落库，继续强制执行打卡")
			}

			c.cmd.Println("  👉 开始执行 [黑胶乐签打卡]...")
			signedToday = c.executeVipSign(ctx, eapiRequest, deviceId)
			if signedToday && beforeTodayScore >= 0 {
				if growth, err := getVipGrowthState(ctx, eapiRequest, deviceId); err == nil && growth.TodayScore != beforeTodayScore {
					c.cmd.Printf("  👉 黑胶今日成长值变化: %d -> %d\n", beforeTodayScore, growth.TodayScore)
				}
			}

			if !signedToday {
				c.cmd.Println("  ❌ 签到验证失败: 未找到今日有效的签到记录 (今日可能仍未成功打卡)")
			}
			continue
		}

		if v.Status == 100 {
			continue
		}

		if v.MissionCode == "HXSSG" || strings.Contains(v.MainTitle, "红心3首") {
			c.cmd.Println("  👉 开始执行 [红心3首VIP单曲]...")
			likedSongIds = c.doVipSongLike(ctx, request, eapiRequest)
		}

		if strings.Contains(v.MainTitle, "调音") {
			c.cmd.Println("  👉 开始执行 [查看AI调音大师]...")
			c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 16, 20, "查看AI调音大师")
		}

		if strings.Contains(v.MainTitle, "云贝") {
			c.cmd.Println("  👉 开始执行 [浏览云贝中心]...")
			c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 16, 20, "浏览云贝中心")
		}

		if strings.Contains(v.MainTitle, "分享") {
			c.cmd.Println("  👉 开始执行 [分享单曲到站外]...")
			c.doVipSimulateBrowse(ctx, cli, request, v.JumpUrl, 3, 5, "分享单曲到站外")
		}

		if v.MissionCode == "FLQ" || strings.Contains(v.MainTitle, "领福利") {
			c.cmd.Println("  👉 开始执行 [免费领福利]...")
			c.doVipWelfareClaim(ctx, request, eapiRequest, userLevel)
		}
	}
	if !hasVipSignTask && !signedToday {
		c.cmd.Println("  ⚠️ 任务列表未返回黑胶乐签条目，但 sign/info 显示今日未落库，直接执行黑胶乐签打卡")
		signedToday = c.executeVipSign(ctx, eapiRequest, deviceId)
		if !signedToday {
			c.cmd.Println("  ❌ 签到验证失败: 未找到今日有效的签到记录 (今日可能仍未成功打卡)")
		}
	}

	// 3. 一键领取所有已完成的成长值
	if c.isAutomatic() {
		c.cmd.Println("  👉 正在一键领取所有黑胶 VIP 成长值...")
		eapiReward, err := eapiRequest.VipRewardGetAll(ctx, &eapi.VipRewardGetAllReq{
			DeviceId: deviceId,
			OS:       "iOS",
			VerifyId: 1,
			Header:   struct{}{},
			ER:       true,
		})
		if err == nil && eapiReward.Code == 200 && eapiReward.Data.Result {
			c.cmd.Println("  ✅ 成功领取所有黑胶 VIP 成长值 (EAPI)")
		} else {
			// EAPI 失败时，回退到 weapi 版
			weapiReward, err := request.VipRewardGetAll(ctx, &weapi.VipRewardGetAllReq{})
			if err == nil && weapiReward.Data.Result {
				c.cmd.Println("  ✅ 成功领取所有黑胶 VIP 成长值 (WEAPI)")
			} else {
				c.cmd.Printf("  ⚠️ 一键领取成长值结果不明确，可能有部分已领取或需手动核对。EAPI: %v, WEAPI: %v\n", err, err)
			}
		}
	}

	// 4. 获取任务列表 (后置对照)
	c.cmd.Println("  👉 获取黑胶 VIP 任务列表...")
	finalList, err := eapiRequest.VipTaskList(ctx, newVipTaskListReq(deviceId))
	if err == nil && finalList.Code == 200 {
		c.cmd.Println("  👉 黑胶 VIP 任务列表 (执行后):")
		for _, v := range finalList.Data {
			statusStr := "未完成"
			if v.Status == 100 {
				statusStr = "已完成"
			}
			// 黑胶乐签仍只认 sign/info 的真实落库状态。
			if isVipSignTask(v) {
				if signedToday {
					statusStr = "已完成"
				} else {
					statusStr = "未完成"
				}
			}
			worth := v.Worth
			if worth == 0 && isVipSignTask(v) {
				worth = 3
			}
			c.cmd.Printf("    - 任务: %-15s | 状态: %-6s | 奖励: %d成长值\n", v.MainTitle, statusStr, worth)
			if isVipSignTask(v) && !signedToday && isVipSignTaskDone(v) {
				c.cmd.Println("      ⚠️ 任务中心仍返回已打卡，但 sign/info 未落库，最终仍按未完成处理")
			}
		}
	} else {
		c.cmd.Printf("  ❌ 无法获取最终对照任务列表: %v\n", err)
	}

	// 4.5. 再次获取并展示今日乐签和成长值状态以作对比
	c.cmd.Println("  👉 获取执行后黑胶 VIP 状态以作对比...")
	if finalGrowth, err := getVipGrowthState(ctx, eapiRequest, deviceId); err == nil {
		c.cmd.Printf("  👉 [执行后] 黑胶成长值最终状态: 今日已获得 %d，当前成长值 %d\n", finalGrowth.TodayScore, finalGrowth.GrowthPoint)
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶成长值最终状态失败: %v\n", err)
	}
	if finalSignState, err := getVipSignState(ctx, eapiRequest, deviceId); err == nil {
		if finalSignState.Today {
			c.cmd.Printf("  👉 [执行后] 乐签最终记录: recordId=%d time=%d score=%d\n", finalSignState.RecordId, finalSignState.Time, finalSignState.Score)
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取黑胶乐签最终记录失败: %v\n", err)
	}

	// 5. 统一取消红心，避免干扰用户歌单
	if len(likedSongIds) > 0 {
		c.cmd.Printf("  👉 统一取消 %d 首热门 VIP 歌曲的红心，彻底恢复用户歌单...\n", len(likedSongIds))
		for _, songIdStr := range likedSongIds {
			_, _ = eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
				TrackId:    songIdStr,
				Like:       "false",
				Time:       "3",
				CheckToken: "",
			})
			sleepSec := 1 + rand.Intn(3) // 1 ~ 3 秒
			time.Sleep(time.Duration(sleepSec) * time.Second)
		}
		c.cmd.Println("  ✅ 统一取消红心完成")
	}
}

// doVipSongLike 专门操作热门 VIP 歌曲，不干扰个人收藏歌单
func (c *SignIn) doVipSongLike(ctx context.Context, request *weapi.Api, eapiRequest *eapi.Api) []string {
	// 获取热门 VIP 歌曲歌单 8402996200
	detail, err := request.PlaylistDetail(ctx, &weapi.PlaylistDetailReq{
		Id: "8402996200",
		N:  "10",
		S:  "0",
	})
	if err != nil || detail.Code != 200 || len(detail.Playlist.TrackIds) == 0 {
		c.cmd.Printf("  ❌ 获取热门 VIP 歌曲歌单失败，取消操作以避嫌: %v\n", err)
		return nil
	}

	trackIds := detail.Playlist.TrackIds
	c.cmd.Printf("  👉 成功获取热门 VIP 歌单，包含 %d 首歌曲，准备随机挑选 3 首进行红心打卡...\n", len(trackIds))

	n := len(trackIds)
	count := 3
	if n < 3 {
		count = n
	}

	// 随机打乱下标选择 3 首歌
	indices := rand.Perm(n)
	var likedIds []string
	successCount := 0
	for i := 0; i < count; i++ {
		idx := indices[i]
		songId := trackIds[idx].Id
		songIdStr := fmt.Sprintf("%d", songId)

		// 1. 红心该热门歌曲
		_, likeErr := eapiRequest.SongLike(ctx, &eapi.SongLikeReq{
			TrackId:    songIdStr,
			Like:       "true",
			Time:       "3",
			CheckToken: "",
		})
		if likeErr != nil {
			c.cmd.Printf("    ❌ [%d/3] 红心歌曲 ID %d 失败: %v\n", i+1, songId, likeErr)
			continue
		}

		// 2. 模拟播放器停留避嫌
		sleepSec := 3 + rand.Intn(8) // 3 ~ 10 秒
		c.cmd.Printf("    ⏳ [%d/3] 歌曲 ID %d 已红心，模拟在播放器停留 %d 秒以避嫌...\n", i+1, songId, sleepSec)

		select {
		case <-ctx.Done():
			c.cmd.Println("    ❌ 任务被取消")
			return likedIds
		case <-time.After(time.Duration(sleepSec) * time.Second):
		}

		likedIds = append(likedIds, songIdStr)
		successCount++
	}
	c.cmd.Printf("  ✅ 红心 3 首 VIP 单曲操作完毕，成功: %d/%d (暂保留红心以待领取奖励)\n", successCount, count)
	return likedIds
}

// doVipSimulateBrowse 模拟请求活动页面并在本地进行停留随机延迟，达成网页停留要求
func (c *SignIn) doVipSimulateBrowse(ctx context.Context, cli *api.Client, request *weapi.Api, jumpUrl string, minSec, maxSec int, taskName string) {
	if jumpUrl == "" {
		c.cmd.Printf("  ⚠️ [%s] 的跳转链接为空，跳过浏览模拟\n", taskName)
		return
	}

	// Ensure jumpUrl has fromRN=1 to indicate App environment
	if !strings.Contains(jumpUrl, "fromRN=") {
		if strings.Contains(jumpUrl, "?") {
			jumpUrl += "&fromRN=1"
		} else {
			jumpUrl += "?fromRN=1"
		}
	}

	// Try parsing task parameters from jumpUrl for page view reporting
	var (
		taskId       string
		taskType     int    = 200
		viewTime     int64  = 15
		taskBusiness string = "music.vip_growth"
		pageCode     string = "music_vip_sound_effect_detail"
	)

	if parsedUrl, err := url.Parse(jumpUrl); err == nil {
		q := parsedUrl.Query()
		if q.Get("view_task_id") != "" {
			taskId = q.Get("view_task_id")
		}
		if q.Get("view_task_business") != "" {
			taskBusiness = q.Get("view_task_business")
		}
		if q.Get("view_time") != "" {
			if vt, err := strconv.ParseInt(q.Get("view_time"), 10, 64); err == nil {
				viewTime = vt
			}
		}
		if q.Get("view_task_type") != "" {
			if tt, err := strconv.Atoi(q.Get("view_task_type")); err == nil {
				taskType = tt
			}
		}

		// Dynamically construct pageCode based on URL path base
		// e.g., /st/vip/sound-effect-detail -> sound_effect_detail -> music_vip_sound_effect_detail
		pathParts := strings.Split(parsedUrl.Path, "/")
		if len(pathParts) > 0 {
			lastPart := pathParts[len(pathParts)-1]
			if lastPart != "" {
				cleaned := strings.ReplaceAll(lastPart, "-", "_")
				pageCode = "music_vip_" + cleaned
			}
		}
	}

	var webkitContext string
	c.cmd.Printf("  👉 模拟加载 [%s] 页面...\n", taskName)
	httpClient := cli.GetClient()
	req, err := http.NewRequestWithContext(ctx, "GET", jumpUrl, nil)
	if err != nil {
		c.cmd.Printf("  ⚠️ [%s] 创建请求失败: %v\n", taskName, err)
	} else {
		webViewId := fmt.Sprintf("%d", 1000000000+rand.Int63n(9000000000))
		escapedHref := strings.ReplaceAll(jumpUrl, "/", "\\/")
		webkitContext = fmt.Sprintf(`{"webViewId":"%s","href":"%s","newebkit":1}`, webViewId, escapedHref)

		req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 CloudMusic/0.1.1 NeteaseMusic/9.4.95")
		req.Header.Set("netease_webkit_context", webkitContext)
		req.Header.Set("Referer", "https://music.163.com/")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh-Hans;q=0.9")

		resp, err := httpClient.Do(req)
		if err != nil {
			c.cmd.Printf("  ⚠️ [%s] 模拟页面请求失败 (但通常不影响后续停留判定): %v\n", taskName, err)
		} else {
			resp.Body.Close()
			c.cmd.Printf("  ℹ️ 页面加载响应码: %d\n", resp.StatusCode)
		}
	}

	// Report viewStart to create a server-side browsing session
	if taskId != "" {
		startBytes, _ := json.Marshal(weapi.VipMiddlePageViewReportData{
			ActionType:   "viewStart",
			Time:         time.Now().UnixNano() / int64(time.Millisecond),
			TaskId:       taskId,
			TaskType:     taskType,
			ViewTime:     0,
			JumpUrl:      jumpUrl,
			TaskBusiness: taskBusiness,
			ResourceType: "vip_growth",
			PageCode:     pageCode,
		})
		_, startErr := request.VipMiddlePageViewReport(ctx, &weapi.VipMiddlePageViewReportReq{
			WebkitContext: webkitContext,
			Data:          string(startBytes),
		})
		if startErr != nil {
			c.cmd.Printf("  ⚠️ [%s] 上报 viewStart 失败: %v\n", taskName, startErr)
		}
	}

	// Stay at least viewTime seconds plus a little random delay
	sleepSec := int(viewTime)
	if minSec > sleepSec {
		sleepSec = minSec
	}
	if maxSec > sleepSec {
		sleepSec = sleepSec + rand.Intn(maxSec-sleepSec+1)
	} else {
		sleepSec = sleepSec + rand.Intn(5) // default 0~4s random delay
	}

	c.cmd.Printf("  ⏳ 正在模拟 [%s] 的页面停留，等待 %d 秒...\n", taskName, sleepSec)
	select {
	case <-ctx.Done():
		c.cmd.Println("  ❌ 浏览模拟被取消")
		return
	case <-time.After(time.Duration(sleepSec) * time.Second):
	}
	c.cmd.Printf("  ✅ [%s] 页面浏览模拟完成\n", taskName)

	// Report page view end to complete the task
	if taskId != "" {
		c.cmd.Printf("  👉 正在上报 [%s] 的浏览完成状态 (taskId: %s, duration: %d秒)...\n", taskName, taskId, sleepSec)
		dataBytes, _ := json.Marshal(weapi.VipMiddlePageViewReportData{
			ActionType:   "viewEnd",
			Time:         time.Now().UnixNano() / int64(time.Millisecond),
			TaskId:       taskId,
			TaskType:     taskType,
			ViewTime:     int64(sleepSec) * 1000,
			JumpUrl:      jumpUrl,
			TaskBusiness: taskBusiness,
			ResourceType: "vip_growth",
			PageCode:     pageCode,
		})
		resp, reportErr := request.VipMiddlePageViewReport(ctx, &weapi.VipMiddlePageViewReportReq{
			WebkitContext: webkitContext,
			Data:          string(dataBytes),
		})
		if reportErr != nil {
			c.cmd.Printf("  ❌ 上报 [%s] 浏览完成状态失败: %v\n", taskName, reportErr)
		} else {
			c.cmd.Printf("  ✅ 上报 [%s] 浏览完成状态成功: code=%d, data=%v, msg=%s\n", taskName, resp.Code, resp.Data, resp.Message)
		}
	}
}

// doVipWelfareClaim 获取会员等级福利列表并自动领取第一个未领福利
// doVipWelfareClaim 获取会员等级福利列表并自动领取第一个未领福利
func (c *SignIn) doVipWelfareClaim(ctx context.Context, request *weapi.Api, eapiRequest *eapi.Api, userLevel int64) {
	// 1. 优先尝试自动领券打卡日常“免费领福利”任务
	c.cmd.Println("  👉 获取当前常驻免费商家福利券列表...")
	benefitList, err := eapiRequest.VipBenefitCategoryList(ctx, &eapi.VipBenefitCategoryListReq{
		Category: "1291816",
		Header:   struct{}{},
	})
	if err == nil && benefitList.Code == 200 && len(benefitList.Data) > 0 {
		var targetBenefitId int64
		var targetBenefitName string
		for _, b := range benefitList.Data {
			if !b.BenefitGet && b.Id > 0 {
				targetBenefitId = b.Id
				targetBenefitName = b.Name
				break
			}
		}

		if targetBenefitId > 0 {
			c.cmd.Printf("  👉 发现尚未领取的商家福利券: [%s] (Id: %d)，开始自动领券打卡...\n", targetBenefitName, targetBenefitId)
			getResp, getErr := eapiRequest.VipBenefitGet(ctx, &eapi.VipBenefitGetReq{
				Id:     fmt.Sprintf("%d", targetBenefitId),
				Header: struct{}{},
			})
			if getErr != nil {
				c.cmd.Printf("  ❌ 领取福利券 [%s] 失败 (网络错误): %v\n", targetBenefitName, getErr)
			} else if getResp.Code != 200 || !getResp.Result.BenefitGet {
				c.cmd.Printf("  ❌ 领取福利券 [%s] 失败: code=%d, msg=%s\n", targetBenefitName, getResp.Code, getResp.Message)
			} else {
				c.cmd.Printf("  🎉 成功领券打卡日常福利任务: [%s]\n", targetBenefitName)
				return // 领券成功即代表日常任务打卡成功，直接返回，不再执行后续降级逻辑
			}
		} else {
			c.cmd.Println("  ℹ️ 所有商家福利券都已领过，跳过自动领券打卡")
		}
	} else {
		c.cmd.Printf("  ⚠️ 获取商家福利券列表失败 (可能是网络波动或接口微调): %v\n", err)
	}

	// 2. 兜底方案：触发 EAPI 版本的福利列表获取，此操作同样可尝试触发日常任务判定
	cli := eapiRequest.Client()
	welfareDeviceId := getDeviceId(cli)

	_, _ = eapiRequest.VipWelfareList(ctx, &eapi.VipWelfareListReq{
		DeviceId: welfareDeviceId,
		OS:       "iOS",
		VerifyId: 1,
		Header:   struct{}{},
	})

	c.cmd.Println("  👉 获取当前可领取的黑胶等级福利列表...")
	welfareList, err := request.VipWelfareList(ctx, &weapi.VipWelfareListReq{})
	if err != nil || welfareList.Code != 200 {
		c.cmd.Printf("  ❌ 获取福利列表失败: %v\n", err)
		return
	}

	var targetWelfareId int64
	var targetWelfareName string

	// 遍历等级 map 寻找 UserReceiveStatus 为 0 (代表未领取) 且 Id > 0 的等级特权
	for levelKey, list := range welfareList.Data {
		var level int64
		if lVal, err := strconv.ParseInt(levelKey, 10, 64); err == nil {
			level = lVal
		}
		if userLevel > 0 && level > userLevel {
			continue
		}

		for _, w := range list {
			if w.UserReceiveStatus == 0 && w.Id > 0 {
				targetWelfareId = w.Id
				targetWelfareName = fmt.Sprintf("%s (等级特权:%s)", w.ShowName, levelKey)
				break
			}
		}
		if targetWelfareId > 0 {
			break
		}
	}

	if targetWelfareId == 0 {
		c.cmd.Println("  ℹ️ 没有找到当前可领取的等级特权福利，可能已全部领取")
		return
	}

	c.cmd.Printf("  👉 发现可领取福利: [%s] (Id: %d)，开始执行领取...\n", targetWelfareName, targetWelfareId)
	claimResp, err := request.VipWelfareClaim(ctx, &weapi.VipWelfareClaimReq{
		WelfareId: targetWelfareId,
	})
	if err != nil {
		c.cmd.Printf("  ❌ 领取福利 [%s] 失败 (网络错误): %v\n", targetWelfareName, err)
	} else if claimResp.Code == 404 {
		c.cmd.Printf("  ℹ️ 福利 [%s] 无法自动领取，可能已被领完或相关活动接口已下线 (404)\n", targetWelfareName)
	} else if claimResp.Code != 200 {
		c.cmd.Printf("  ❌ 领取福利 [%s] 失败 (服务限制): code=%d, msg=%s\n", targetWelfareName, claimResp.Code, claimResp.Message)
	} else {
		c.cmd.Printf("  🎉 成功领取尊享等级福利: [%s]\n", targetWelfareName)
	}
}
