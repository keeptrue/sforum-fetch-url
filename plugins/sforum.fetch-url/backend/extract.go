package main

import (
	"bytes"
	"mime"
	"regexp"
	"strings"

	stdhtml "html"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// textMediaTypes 是可以安全交给模型的内容类型。
// 二进制一律拒绝：把 PDF、图片或压缩包喂进上下文既没有意义，也是在替供应商的
// 解码器做未经验证的假设。
var textMediaTypes = map[string]struct{}{
	"text/html": {}, "application/xhtml+xml": {},
	"text/plain": {}, "text/markdown": {}, "text/x-markdown": {},
	"text/csv": {}, "text/tab-separated-values": {},
	"application/json": {}, "application/ld+json": {}, "application/x-ndjson": {},
	"application/xml": {}, "text/xml": {},
	"application/rss+xml": {}, "application/atom+xml": {},
}

// htmlMediaTypes 需要走「剥标签」抽取；其余文本类型原样返回。
var htmlMediaTypes = map[string]struct{}{
	"text/html": {}, "application/xhtml+xml": {}, "application/rss+xml": {}, "application/atom+xml": {},
}

var (
	blockTagPattern   = regexp.MustCompile(`(?i)</?(?:p|div|section|article|header|footer|main|aside|nav|ul|ol|dl|table|thead|tbody|tfoot|tr|blockquote|pre|figure|figcaption|form|fieldset|address|hr|h[1-6]|li|dt|dd|br)\b[^>]*>`)
	anyTagPattern     = regexp.MustCompile(`(?s)<[^>]*>`)
	commentPattern    = regexp.MustCompile(`(?s)<!--.*?-->`)
	scriptPattern     = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`)
	stylePattern      = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`)
	noscriptPattern   = regexp.MustCompile(`(?is)<noscript\b.*?</noscript\s*>`)
	templatePattern   = regexp.MustCompile(`(?is)<template\b.*?</template\s*>`)
	svgPattern        = regexp.MustCompile(`(?is)<svg\b.*?</svg\s*>`)
	titlePattern      = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	metaCharsetInBody = regexp.MustCompile(`(?is)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9_\-:.]+)`)
	multiBlankLine    = regexp.MustCompile(`\n{3,}`)
	// Go 的 RE2 语法不支持 \uXXXX，必须写 \x{...}；否则 MustCompile 会在
	// 包初始化时 panic，插件连握手都进不去。
	multiSpace = regexp.MustCompile(`[ \t\f\v\x{00a0}]{2,}`)
)

// extractText 把响应体转成给模型看的纯文本。
//
// 返回的第二个值是抽取到的页面标题（HTML 才有），它进元信息行，不进正文。
func extractText(contentType string, body []byte) (string, string, error) {
	mediaType := normalizeMediaType(contentType)
	if mediaType == "" {
		// 没有 Content-Type：按纯文本处理。这比拒绝更实用——很多内网文档站
		// 就是不设头，而按文本处理的最坏结果是模型看到一堆标签。
		mediaType = "text/plain"
	}
	if _, ok := textMediaTypes[mediaType]; !ok {
		return "", "", fail("目标返回的是 %s，不是可读文本，已跳过。", mediaType)
	}
	decoded, err := decodeBody(contentType, body)
	if err != nil {
		return "", "", err
	}
	if _, ok := htmlMediaTypes[mediaType]; !ok {
		return normalizeText(string(decoded)), "", nil
	}
	title := ""
	if match := titlePattern.FindSubmatch(decoded); match != nil {
		title = normalizeText(stdhtml.UnescapeString(string(match[1])))
	}
	return normalizeText(htmlToText(decoded)), title, nil
}

func normalizeMediaType(contentType string) string {
	trimmed := strings.TrimSpace(contentType)
	if trimmed == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		if index := strings.Index(trimmed, ";"); index >= 0 {
			trimmed = trimmed[:index]
		}
		return strings.ToLower(strings.TrimSpace(trimmed))
	}
	return strings.ToLower(mediaType)
}

// decodeBody 把响应体解码成 UTF-8。字符集来自 Content-Type 的 charset 参数，
// 缺失时回落到 HTML 里的 <meta charset>——中文站点大量依赖后者。
func decodeBody(contentType string, body []byte) ([]byte, error) {
	charset := charsetFromContentType(contentType)
	if charset == "" {
		charset = sniffMetaCharset(body)
	}
	decoder := decoderForCharset(charset)
	if decoder == nil {
		return stripBOM(body), nil
	}
	decoded, _, err := transform.Bytes(decoder, body)
	if err != nil {
		// 解码失败不是致命问题：退化成「按原字节当 UTF-8 看」，比整次抓取失败好。
		return stripBOM(body), nil
	}
	return stripBOM(decoded), nil
}

func charsetFromContentType(contentType string) string {
	_, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(params["charset"]))
}

func sniffMetaCharset(body []byte) string {
	limit := len(body)
	if limit > 4096 {
		limit = 4096
	}
	match := metaCharsetInBody.FindSubmatch(body[:limit])
	if match == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(match[1])))
}

func decoderForCharset(charset string) *encoding.Decoder {
	switch strings.ReplaceAll(charset, "_", "-") {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return nil
	case "gb2312", "gbk", "gb18030", "x-gbk", "csgb2312":
		return simplifiedchinese.GB18030.NewDecoder()
	case "big5", "big5-hkscs", "cp950":
		return traditionalchinese.Big5.NewDecoder()
	case "shift-jis", "shift_jis", "sjis", "x-sjis", "cp932":
		return japanese.ShiftJIS.NewDecoder()
	case "euc-jp", "x-euc-jp":
		return japanese.EUCJP.NewDecoder()
	case "euc-kr", "ks_c_5601-1987", "cp949":
		return korean.EUCKR.NewDecoder()
	case "utf-16", "utf-16le", "utf16":
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
	case "utf-16be":
		return unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()
	case "iso-8859-1", "latin1", "latin-1":
		return charmap.ISO8859_1.NewDecoder()
	case "windows-1252", "cp1252":
		return charmap.Windows1252.NewDecoder()
	default:
		return nil
	}
}

func stripBOM(body []byte) []byte {
	return bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
}

// htmlToText 把 HTML 收敛成可读纯文本。
//
// 顺序很重要：先摘掉整块「不是正文」的元素（否则 script 里的 JS 会被当成正文），
// 再把块级标签换成换行（否则整页会粘成一行），最后才剥剩余标签并解实体。
func htmlToText(body []byte) string {
	value := commentPattern.ReplaceAll(body, nil)
	for _, pattern := range []*regexp.Regexp{scriptPattern, stylePattern, noscriptPattern, templatePattern, svgPattern} {
		value = pattern.ReplaceAll(value, nil)
	}
	value = blockTagPattern.ReplaceAll(value, []byte("\n"))
	value = anyTagPattern.ReplaceAll(value, nil)
	return stdhtml.UnescapeString(string(value))
}

// normalizeText 压缩空白：合并多余空格、折叠连续空行、去掉行尾空白。
// 页面里真正占字节的是导航与空白，不压缩就等于把上下文预算烧在格式上。
//
// NBSP（U+00A0）先归一成普通空格：它在提示词里看起来与空格一模一样，但
// 分词器常常不把它当分隔符，留着只会让模型读到「World here」这样的粘连词。
func normalizeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\u00a0", " ")
	value = multiSpace.ReplaceAllString(value, " ")
	lines := strings.Split(value, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		cleaned = append(cleaned, strings.TrimRight(strings.TrimSpace(line), " "))
	}
	value = strings.Join(cleaned, "\n")
	value = multiBlankLine.ReplaceAllString(value, "\n\n")
	return strings.TrimSpace(value)
}
