package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sourceLookupMinPattern 是检索串的最小长度，过短会命中大量无关代码。
const sourceLookupMinPattern = 6

// sourceLookupMaxMatches 限制返回的命中数，避免冲爆上下文。
const sourceLookupMaxMatches = 5

// sourceExtensions 是参与检索的源码后缀。
var sourceExtensions = map[string]bool{
	".cc": true, ".h": true, ".c": true, ".py": true, ".hpp": true, ".cxx": true,
}

// SourceMatch 是一处源码命中。
type SourceMatch struct {
	File    string `json:"file"`    // 相对于解析出的源码目录
	Line    int    `json:"line"`    // 行号，从 1 开始
	Snippet string `json:"snippet"` // 去掉首尾空白的命中行
}

// SourceLookupData 是 ceph_source_lookup 的结构化结果。
type SourceLookupData struct {
	Pattern string        `json:"pattern"`
	Version string        `json:"version"`
	Dir     string        `json:"dir"` // 实际检索的源码目录
	Matches []SourceMatch `json:"matches"`
}

type sourceLookupTool struct {
	r      Runner
	srcDir string
}

// NewSourceLookup 构造 ceph_source_lookup 工具；srcDir 来自 --ceph-src。
func NewSourceLookup(r Runner, srcDir string) Tool {
	return &sourceLookupTool{r: r, srcDir: srcDir}
}

func (t *sourceLookupTool) Name() string { return "ceph_source_lookup" }

func (t *sourceLookupTool) Description() string {
	return "在本地 Ceph 源码目录中按日志片段检索，定位该字符串在源码中的文件与行号，用于理解某条日志的确切触发条件。只读本地文件系统，不访问集群。仅当配置了源码目录（--ceph-src）时可用。"
}

func (t *sourceLookupTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","minLength":6,"description":"日志中的特征字符串，至少 6 个字符，如 \"slow request\""}},"required":["pattern"],"additionalProperties":false}`)
}

func (t *sourceLookupTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	pattern := strings.TrimSpace(in.Pattern)
	if len(pattern) < sourceLookupMinPattern {
		return nil, nil, fmt.Errorf("pattern 太短（至少 %d 个字符），否则会命中大量无关代码", sourceLookupMinPattern)
	}
	if strings.ContainsAny(pattern, "\n\r\x00") {
		return nil, nil, fmt.Errorf("pattern 含有非法字符")
	}
	if t.srcDir == "" {
		return nil, nil, fmt.Errorf("未配置 Ceph 源码目录（--ceph-src），源码反查不可用")
	}

	version := t.r.Capabilities(ctx, primaryNode(t.r)).CephVersion
	dir, err := resolveSourceDir(t.srcDir, version)
	if err != nil {
		return nil, nil, err
	}

	matches, searchOut, err := searchSource(ctx, dir, pattern, sourceLookupMaxMatches)
	if err != nil {
		return nil, nil, err
	}

	d := &SourceLookupData{Pattern: pattern, Version: version, Dir: dir, Matches: matches}
	raw := &Raw{
		Node:    "local",
		Command: fmt.Sprintf("grep -rn %q %s", pattern, dir),
		Output:  searchOut,
	}

	res := &Result{Data: d}
	if len(matches) == 0 {
		res.Summary = fmt.Sprintf("在 Ceph %s 源码（%s）中未找到 %q", version, dir, pattern)
		res.Hints = append(res.Hints, "源码中未命中，日志可能来自第三方库（如 Boost/LevelDB）或已被改写，可换用更短的特征串重试")
		return res, raw, nil
	}

	var where []string
	for _, m := range matches {
		where = append(where, fmt.Sprintf("%s:%d", m.File, m.Line))
	}
	res.Summary = fmt.Sprintf("在 Ceph %s 源码中找到 %d 处 %q 的使用：%s", version, len(matches), pattern, strings.Join(where, "、"))
	res.Hints = append(res.Hints, "可依据源码上下文判断该日志的确切触发条件，再回到运行时数据验证")
	if len(matches) >= sourceLookupMaxMatches {
		res.Summary += fmt.Sprintf("（已达上限 %d 处，可能还有更多）", sourceLookupMaxMatches)
	}
	return res, raw, nil
}

// resolveSourceDir 在源码根目录下按 v<ver>、<ver>、ceph-<ver>、根目录的顺序查找版本目录，首个存在者胜出。
// 只有当根目录本身看起来像 Ceph 源码（含 src/ 且 src/ 下有 .cc 文件）时才回退到根目录，
// 否则只配置了一个父目录时会误把该目录当成源码树检索。
func resolveSourceDir(root, version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("无法确定 Ceph 版本（能力探测未返回版本号），源码反查不可用")
	}
	candidates := []string{
		filepath.Join(root, "v"+version),
		filepath.Join(root, version),
		filepath.Join(root, "ceph-"+version),
	}
	if looksLikeCephSource(root) {
		candidates = append(candidates, root)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("在 %s 下找不到版本 %s 的源码目录（已尝试 v%s、%s、ceph-%s）", root, version, version, version, version)
}

// searchSource 递归检索源码，返回命中位置及 file:line:text 形式的原始输出。
// 达到 maxHits 即停止（filepath.SkipAll），并响应 ctx 取消。
func searchSource(ctx context.Context, dir, pattern string, maxHits int) ([]SourceMatch, string, error) {
	var matches []SourceMatch
	var sb strings.Builder

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过不可读的文件/目录
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExtensions[filepath.Ext(path)] {
			return nil
		}

		fileMatches, err := scanSourceFile(dir, path, pattern)
		if err != nil {
			return nil // 读失败不影响其余文件
		}
		for _, m := range fileMatches {
			matches = append(matches, m)
			fmt.Fprintf(&sb, "%s:%d:%s\n", m.File, m.Line, m.Snippet)
			if len(matches) >= maxHits {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("源码检索被取消: %w", walkErr)
		}
		return nil, "", fmt.Errorf("源码检索失败: %w", walkErr)
	}
	return matches, sb.String(), nil
}

// looksLikeCephSource 粗判 root 本身是否就是 Ceph 源码树：存在 src/ 子目录，
// 且其中有 .cc 文件。最多检查几层，避免在大型目录上遍历过多。
func looksLikeCephSource(root string) bool {
	src := filepath.Join(root, "src")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return false
	}
	found := false
	filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".cc" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// scanSourceFile 在单个文件里找出包含 pattern 的行。
func scanSourceFile(root, path, pattern string) ([]SourceMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}

	var out []SourceMatch
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineno := 0
	for sc.Scan() {
		lineno++
		line := sc.Text()
		if !strings.Contains(line, pattern) {
			continue
		}
		out = append(out, SourceMatch{File: rel, Line: lineno, Snippet: strings.TrimSpace(line)})
	}
	return out, sc.Err()
}
