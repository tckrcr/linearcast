package liveproxy

import (
	"net/url"
	"path"
	"strings"
)

// RewriteManifest rewrites HLS line URIs and quoted URI attributes. The rewrite
// function receives the raw manifest URI and the path of the manifest being
// rewritten, and returns false when the URI should be left unchanged.
func RewriteManifest(body []byte, sourcePath string, rewrite func(raw, sourcePath string) (string, bool)) []byte {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = RewriteURIAttributes(line, func(raw string) string {
				if rewritten, ok := rewrite(raw, sourcePath); ok {
					return rewritten
				}
				return raw
			})
			continue
		}
		prefixLen := len(line) - len(strings.TrimLeft(line, " \t"))
		suffixLen := len(line) - len(strings.TrimRight(line, " \t\r"))
		prefix := line[:prefixLen]
		suffix := line[len(line)-suffixLen:]
		if rewritten, ok := rewrite(trimmed, sourcePath); ok {
			lines[i] = prefix + rewritten + suffix
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// RewriteURIAttributes rewrites each quoted URI="..." attribute in an HLS tag.
func RewriteURIAttributes(line string, rewrite func(string) string) string {
	const key = `URI="`
	var b strings.Builder
	rest := line
	for {
		idx := strings.Index(rest, key)
		if idx < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:idx+len(key)])
		rest = rest[idx+len(key):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rewrite(rest[:end]))
		rest = rest[end:]
	}
}

// RelativePath resolves a manifest URI to a proxy-safe relative path. Absolute
// URLs are intentionally rejected here; adapters that trust specific absolute
// upstreams can normalize them before calling this helper.
func RelativePath(raw, sourcePath string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	if u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return "", false
	}

	rel := u.Path
	if sourcePath != "" {
		base := path.Dir(strings.TrimPrefix(sourcePath, "/"))
		rel = path.Clean(path.Join(base, u.Path))
	}
	if !SafeRelativePath(rel) {
		return "", false
	}
	return AppendQuery(rel, u.RawQuery), true
}

// SafeRelativePath rejects empty, absolute, parent-traversing, and Windows-style
// paths before they are embedded in proxy routes.
func SafeRelativePath(rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return false
	}
	clean := path.Clean(rel)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// RelativeReference expresses target as a path relative to baseDir, where both
// are slash-separated paths rooted at the same base (baseDir "" or "." means
// that root). The result has no leading slash, so a manifest served at baseDir
// can reference target while preserving any reverse-proxy mount prefix (e.g.
// /hls) present on the manifest's own URL. Differing branches are walked back up
// with "..".
func RelativeReference(baseDir, target string) string {
	baseDir = strings.Trim(strings.TrimSpace(baseDir), "/")
	target = strings.TrimPrefix(strings.TrimSpace(target), "/")
	if baseDir == "" || baseDir == "." {
		return target
	}
	baseParts := strings.Split(baseDir, "/")
	targetParts := strings.Split(target, "/")
	i := 0
	for i < len(baseParts) && i < len(targetParts) && baseParts[i] == targetParts[i] {
		i++
	}
	out := make([]string, 0, len(baseParts)-i+len(targetParts)-i)
	for j := i; j < len(baseParts); j++ {
		out = append(out, "..")
	}
	out = append(out, targetParts[i:]...)
	return strings.Join(out, "/")
}

// AppendQuery appends a raw query string to rel after dropping any denied keys.
func AppendQuery(rel, rawQuery string, denyKeys ...string) string {
	if rawQuery == "" {
		return rel
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rel + "?" + rawQuery
	}
	for _, deny := range denyKeys {
		for key := range q {
			if strings.EqualFold(key, deny) {
				q.Del(key)
			}
		}
	}
	if enc := q.Encode(); enc != "" {
		return rel + "?" + enc
	}
	return rel
}
