package observability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/report"
)

// deadlineRejectionWindow 是“周期建立前截止已过”拒绝的告警窗口：最近这段时间内出现过就上报关注项。
const deadlineRejectionWindow = 10 * time.Minute

// DeadlineRejections 在内存里记每个数据集最近一次“周期建立前截止已过”拒绝的时刻，采集指标时把最近 10 分钟内有过拒绝的
// 数据集上报为关注项 deadline_rejected：写入方取数晚于数据集截止时整期提交都会被拒（常见于采集方式切换后截止没跟上），
// 数据只能靠补采补齐。记录数以数据集为界，超过上限的不再新建；storage-primary 重启后记录清空。
type DeadlineRejections struct {
	maxItems int

	mu     sync.Mutex
	latest map[[3]string]time.Time
}

// NewDeadlineRejections 把截止拒绝登记成 attention 的一个采集时现算的来源；attention 为空时只记不报（测试用）。
func NewDeadlineRejections(attention *report.Attention) *DeadlineRejections {
	d := &DeadlineRejections{maxItems: MaxDatasetSeries, latest: map[[3]string]time.Time{}}
	if attention != nil {
		attention.AddSource("deadline_rejections", d.attention)
	}
	return d
}

func (d *DeadlineRejections) ObserveDeadlineRejection(spaceID, datasetID, frequency string, at time.Time) {
	if d == nil || spaceID == "" || datasetID == "" {
		return
	}
	key := [3]string{spaceID, datasetID, frequency}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.latest[key]; !ok && len(d.latest) >= d.maxItems {
		return
	}
	d.latest[key] = at.UTC()
}

// attention 列出最近 10 分钟内有过截止拒绝的数据集：<空间>/<数据集 ID>（<频率>，最近 hh:mm）。
func (d *DeadlineRejections) attention(now time.Time) map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var recent []string
	for key, at := range d.latest {
		if now.Sub(at) > deadlineRejectionWindow {
			continue
		}
		recent = append(recent, fmt.Sprintf("%s/%s（%s，最近 %s）", key[0], key[1], key[2], report.AttentionTime(at)))
	}
	if len(recent) == 0 {
		return nil
	}
	sort.Strings(recent)
	// 合成一条：说明在前、对象按序在后（多条会被重新排序）。
	detail := "周期建立前截止已过（写入方取数晚于数据集截止，常见于采集方式切换后截止没跟上），整期数据要靠补采：" + strings.Join(recent, "；")
	return map[string][]string{AttentionDeadlineRejected: {detail}}
}
