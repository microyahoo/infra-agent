// Package ceph 提供 Ceph 数据结构的解析与摘要，供工具层与会话快照共用。
package ceph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Status 是 `ceph status -f json` 中与诊断相关的字段。
type Status struct {
	FSID           string    `json:"fsid"`
	Health         string    `json:"health"`
	HealthChecks   []string  `json:"health_checks,omitempty"`
	HealthMessages []string  `json:"health_messages,omitempty"`
	NumMons        int       `json:"num_mons"`
	NumOSDs        int       `json:"num_osds"`
	NumOSDsUp      int       `json:"num_osds_up"`
	NumOSDsIn      int       `json:"num_osds_in"`
	NumPGs         int       `json:"num_pgs"`
	DegradedPGs    int       `json:"degraded_pgs"`
	PGState        []PGState `json:"pg_state"`
	BytesTotal     uint64    `json:"bytes_total"`
	BytesUsed      uint64    `json:"bytes_used"`
	UsedPercent    float64   `json:"used_percent"`
}

// PGState 是单个 PG 状态及其数量。
type PGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type rawStatus struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string                     `json:"status"`
		Checks map[string]json.RawMessage `json:"checks"`
	} `json:"health"`
	Monmap struct {
		NumMons int `json:"num_mons"`
	} `json:"monmap"`
	OSDMap struct {
		NumOSDs   int `json:"num_osds"`
		NumUpOSDs int `json:"num_up_osds"`
		NumInOSDs int `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		NumPGs     int `json:"num_pgs"`
		PGsByState []struct {
			StateName string `json:"state_name"`
			Count     int    `json:"count"`
		} `json:"pgs_by_state"`
		BytesTotal uint64 `json:"bytes_total"`
		BytesUsed  uint64 `json:"bytes_used"`
	} `json:"pgmap"`
}

// ParseStatus 解析 ceph status JSON。
func ParseStatus(raw string) (Status, error) {
	var rs rawStatus
	if err := json.Unmarshal([]byte(raw), &rs); err != nil {
		return Status{}, err
	}

	out := Status{
		FSID:       strings.TrimSpace(rs.FSID),
		Health:     strings.TrimSpace(rs.Health.Status),
		NumMons:    rs.Monmap.NumMons,
		NumOSDs:    rs.OSDMap.NumOSDs,
		NumOSDsUp:  rs.OSDMap.NumUpOSDs,
		NumOSDsIn:  rs.OSDMap.NumInOSDs,
		NumPGs:     rs.PGMap.NumPGs,
		BytesTotal: rs.PGMap.BytesTotal,
		BytesUsed:  rs.PGMap.BytesUsed,
	}

	// checks 是 map，遍历顺序不确定，排序后保证摘要稳定可复现。
	for name, rawCheck := range rs.Health.Checks {
		out.HealthChecks = append(out.HealthChecks, name)
		var c struct {
			Summary struct {
				Message string `json:"message"`
			} `json:"summary"`
		}
		if json.Unmarshal(rawCheck, &c) == nil && c.Summary.Message != "" {
			out.HealthMessages = append(out.HealthMessages, c.Summary.Message)
		}
	}
	sort.Strings(out.HealthChecks)
	sort.Strings(out.HealthMessages)

	for _, s := range rs.PGMap.PGsByState {
		out.PGState = append(out.PGState, PGState{State: s.StateName, Count: s.Count})
		if s.StateName != "active+clean" {
			out.DegradedPGs += s.Count
		}
	}
	if out.BytesTotal > 0 {
		out.UsedPercent = float64(out.BytesUsed) / float64(out.BytesTotal) * 100
	}
	return out, nil
}

// Summary 返回一句紧凑的中文概览。
func (s Status) Summary() string {
	return fmt.Sprintf("mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %s",
		s.NumMons, s.NumOSDs, s.NumOSDsUp, s.NumOSDsIn, s.NumPGs, s.DegradedPGs, FormatPercent(s.UsedPercent))
}

// FormatPercent 按数值大小选择有效位数：大容量集群早期使用率极低，
// 固定 %.1f 会显示成 0.0%，看起来像数据缺失。
func FormatPercent(p float64) string {
	switch {
	case p == 0:
		return "0%"
	case p < 0.01:
		return fmt.Sprintf("%.4f%%", p)
	case p < 1:
		return fmt.Sprintf("%.2f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}
