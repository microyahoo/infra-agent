package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// exploreTool 让模型自己发现"信息在哪"：列目录、找日志、看数据目录。
//
// 为什么需要它：日志与数据的位置随部署方式（cephadm / Rook / 包安装）、
// Rook 版本、命名空间而变。靠程序内置候选路径去猜，永远会漏；
// 让模型先探一下目录结构，再决定读哪个文件，才是可持续的做法。
type exploreTool struct{ r Runner }

// NewExplore 构造 explore 工具。
func NewExplore(r Runner) Tool { return &exploreTool{r: r} }

func (t *exploreTool) Name() string { return "explore" }

func (t *exploreTool) Description() string {
	return "查看某个目录里有什么、或找出符合模式的日志文件。" +
		"当你不知道日志/数据在哪里时先用它探路，而不是猜一个路径去读。" +
		"例如：列出 /var/lib/rook/rook-ceph 下的内容；或在 /var/log 下找 *.log。" +
		"只读，只返回文件名与大小，不返回文件内容。"
}

// exploreRoots 是允许探查的根目录。
// 诊断只需要看这些位置；不开放全盘遍历，避免误触敏感目录。
var exploreRoots = []string{
	"/var/log",
	"/var/lib/rook",
	"/var/lib/ceph",
	"/etc/ceph",
	"/run/ceph",
	"/var/crash",
}

func (t *exploreTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "node":{"type":"string","description":"节点名，默认入口节点"},
        "dir":{"type":"string","description":"要列出的目录，例如 /var/lib/rook/rook-ceph"},
        "find_pattern":{"type":"string","description":"改为查找文件名模式，例如 *.log 或 ceph-osd.*.log"}
      },
      "additionalProperties":false
    }`)
}

func (t *exploreTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node        string `json:"node"`
		Dir         string `json:"dir"`
		FindPattern string `json:"find_pattern"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.Dir == "" && in.FindPattern == "" {
		return nil, nil, fmt.Errorf("必须提供 dir 或 find_pattern 之一")
	}

	node := strings.TrimSpace(in.Node)
	if node == "" {
		node = primaryNode(t.r)
	}
	if !knownNode(t.r, node) {
		return nil, nil, fmt.Errorf("未知节点 %q，可先调用 osd_tree 获取节点名", node)
	}

	if in.FindPattern != "" {
		return t.find(ctx, node, in.Dir, in.FindPattern)
	}
	return t.list(ctx, node, in.Dir)
}

// list 列出一个目录的内容。
func (t *exploreTool) list(ctx context.Context, node, dir string) (*Result, *Raw, error) {
	if !underExploreRoot(dir) {
		return nil, nil, fmt.Errorf("目录 %q 不在可探查范围内（允许的根目录：%s）",
			dir, strings.Join(exploreRoots, "、"))
	}

	// ls -la 给出名称、大小、类型，足够模型判断该读哪个。
	argv := []string{"ls", "-la", dir}
	out, err := t.r.Run(ctx, node, argv)
	raw := newRaw(node, argv, out)
	if err != nil {
		return nil, raw, fmt.Errorf("列出 %s 失败: %w", dir, err)
	}

	entries := parseLsEntries(out.Stdout)
	d := &ExploreData{Dir: dir, Node: node, Entries: entries}
	res := &Result{Data: d}

	if len(entries) == 0 {
		res.Summary = fmt.Sprintf("%s:%s 为空或不可读", node, dir)
		return res, raw, nil
	}

	dirs, files := 0, 0
	var logFiles []string
	var subdirs []string
	for _, e := range entries {
		if e.IsDir {
			dirs++
			subdirs = append(subdirs, e.Name)
			continue
		}
		files++
		if strings.Contains(e.Name, ".log") {
			logFiles = append(logFiles, path.Join(dir, e.Name))
		}
	}
	res.Summary = fmt.Sprintf("%s:%s 有 %d 个子目录、%d 个文件", node, dir, dirs, files)
	if len(logFiles) > 0 {
		res.Summary += fmt.Sprintf("，其中日志文件 %d 个", len(logFiles))
	}

	// 指向下一步：让人/模型知道往哪钻，而不是继续猜。
	var hints []string
	for _, sd := range subdirs {
		hints = append(hints, fmt.Sprintf("子目录 %s，可继续 explore(dir=%q)", path.Join(dir, sd), path.Join(dir, sd)))
	}
	if len(logFiles) > 0 {
		hints = append(hints, fmt.Sprintf("可直接 read_log(path=%q) 读取", logFiles[0]))
	}
	if len(hints) > 6 {
		hints = hints[:6]
	}
	res.Hints = hints
	return res, raw, nil
}

// find 在目录下按文件名模式查找。
func (t *exploreTool) find(ctx context.Context, node, dir, pattern string) (*Result, *Raw, error) {
	if dir == "" {
		dir = "/var/log"
	}
	if !underExploreRoot(dir) {
		return nil, nil, fmt.Errorf("目录 %q 不在可探查范围内（允许的根目录：%s）",
			dir, strings.Join(exploreRoots, "、"))
	}
	if strings.ContainsAny(pattern, "/ \t") {
		return nil, nil, fmt.Errorf("find_pattern 只能是不含路径分隔符的文件名模式，例如 *.log")
	}

	argv := []string{"find", dir, "-name", pattern, "-type", "f", "-printf", "%s %p\n"}
	out, err := t.r.Run(ctx, node, argv)
	raw := newRaw(node, argv, out)
	if err != nil {
		return nil, raw, fmt.Errorf("查找失败: %w", err)
	}

	var found []FileEntry
	for _, line := range strings.Split(strings.TrimSpace(out.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var size int64
		var name string
		if n, _ := fmt.Sscanf(line, "%d", &size); n == 1 {
			if i := strings.Index(line, " "); i > 0 {
				name = strings.TrimSpace(line[i:])
			}
		}
		if name == "" {
			continue
		}
		found = append(found, FileEntry{Name: name, Size: size})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Size > found[j].Size })

	const maxResults = 40
	truncated := false
	if len(found) > maxResults {
		found = found[:maxResults]
		truncated = true
	}

	d := &ExploreData{Dir: dir, Node: node, Pattern: pattern, Files: found, Truncated: truncated}
	res := &Result{Data: d}
	if len(found) == 0 {
		res.Summary = fmt.Sprintf("%s:%s 下没有匹配 %s 的文件", node, dir, pattern)
		res.Hints = []string{fmt.Sprintf("换一个更宽的模式，或先 explore(dir=%q) 看目录结构", dir)}
		return res, raw, nil
	}
	res.Summary = fmt.Sprintf("%s:%s 下匹配 %s 的文件 %d 个（按大小降序）", node, dir, pattern, len(found))
	res.Hints = []string{fmt.Sprintf("最近的/最大的可先用 read_log(path=%q) 读取", found[0].Name)}
	return res, raw, nil
}

// ExploreData 是 explore 的结构化结果。
type ExploreData struct {
	Node      string      `json:"node"`
	Dir       string      `json:"dir"`
	Pattern   string      `json:"pattern,omitempty"`
	Entries   []LsEntry   `json:"entries,omitempty"`
	Files     []FileEntry `json:"files,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}

// LsEntry 是目录中的一项。
type LsEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// FileEntry 是查找到的文件。
type FileEntry struct {
	Name string `json:"path"`
	Size int64  `json:"size"`
}

// underExploreRoot 判断路径是否落在允许探查的根目录下。
func underExploreRoot(p string) bool {
	if strings.Contains(p, "..") {
		return false
	}
	clean := path.Clean(p)
	for _, root := range exploreRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

// parseLsEntries 解析 ls -la 输出，跳过 total 行与 . / .. 项。
func parseLsEntries(out string) []LsEntry {
	var entries []LsEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		// 权限 链接数 属主 属组 大小 ... 名称
		perm := fields[0]
		if perm == "" {
			continue
		}
		name := strings.Join(fields[8:], " ")
		if name == "." || name == ".." {
			continue
		}
		// 符号链接形如 "-> target"，只留名字
		if i := strings.Index(name, " -> "); i > 0 {
			name = name[:i]
		}
		var size int64
		fmt.Sscanf(fields[4], "%d", &size)
		entries = append(entries, LsEntry{
			Name:  name,
			IsDir: strings.HasPrefix(perm, "d"),
			Size:  size,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}
