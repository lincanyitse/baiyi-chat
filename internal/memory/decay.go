package memory

import (
	"context"
	"fmt"
	"time"
)

// Decayer 执行记忆的衰减与归档。
//
// 归档不等于删除：低于阈值的情节不再预召回，但仍可被 memory_search 查到。
// 这是「用户显式写入的记忆永不自动遗忘」与「旧记忆别占上下文」的折中。
type Decayer struct {
	repo *Repo
	now  func() time.Time

	// archiveAfter 是情节不被访问多久后归档。
	archiveAfter time.Duration
	// minScore 低于此得分归档。
	minScore float64
}

// DecayerOptions 配置衰减器。
type DecayerOptions struct {
	ArchiveAfter time.Duration
	MinScore     float64
}

// NewDecayer 构造衰减器。
func NewDecayer(repo *Repo, opts DecayerOptions) *Decayer {
	if opts.ArchiveAfter <= 0 {
		opts.ArchiveAfter = 180 * 24 * time.Hour
	}
	if opts.MinScore <= 0 {
		opts.MinScore = 0.05
	}
	return &Decayer{repo: repo, now: time.Now, archiveAfter: opts.ArchiveAfter, minScore: opts.MinScore}
}

// Run 归档一批陈旧情节，返回归档数量。
//
// 只处理情节，不碰事实：用户明确说过的事实不该因为「很久没提」而消失。
func (d *Decayer) Run(ctx context.Context) (int, error) {
	now := d.now()
	eps, err := d.repo.RecentEpisodes(ctx, 500)
	if err != nil {
		return 0, err
	}

	archived := 0
	for _, e := range eps {
		age := now.Sub(e.CreatedAt)
		if age < d.archiveAfter {
			continue
		}
		if Scoring(e, now) >= d.minScore {
			continue
		}
		if err := d.repo.ArchiveEpisode(ctx, e.ID); err != nil {
			return archived, err
		}
		_ = d.repo.LogEvent(ctx, Event{
			EpisodeID: e.ID,
			Action:    "archive",
			Detail: fmt.Sprintf("超过 %d 天未访问且得分低于 %.2f",
				int(age.Hours()/24), d.minScore),
		})
		archived++
	}
	return archived, nil
}
