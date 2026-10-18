package main

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestExtractTextStripsMarkupAndDecodesEntities(t *testing.T) {
	body := []byte(`<html><head><title>标题 &amp; 副标题</title>` +
		`<style>body{color:red}</style></head>` +
		`<body><script>alert('x')</script>` +
		`<h1>Hello</h1><p>World&nbsp;here</p><div>Second<br>line</div>` +
		`<noscript>ignore me</noscript><svg><text>vector</text></svg>` +
		`<ul><li>one</li><li>two</li></ul></body></html>`)

	text, title, err := extractText("text/html; charset=utf-8", body)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if title != "标题 & 副标题" {
		t.Fatalf("title = %q", title)
	}
	for _, forbidden := range []string{"alert", "color:red", "ignore me", "vector", "<", ">", "&amp;"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("extracted text still contains %q:\n%s", forbidden, text)
		}
	}
	for _, expected := range []string{"Hello", "World here", "Second", "line", "one", "two"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("extracted text missing %q:\n%s", expected, text)
		}
	}
}

func TestExtractTextKeepsNonHTMLTextVerbatim(t *testing.T) {
	body := []byte(`{"a": "<b>not markup to strip</b>", "n": 1}`)
	text, title, err := extractText("application/json", body)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if title != "" {
		t.Fatalf("json produced a title: %q", title)
	}
	if !strings.Contains(text, "<b>not markup to strip</b>") {
		t.Fatalf("json body was altered:\n%s", text)
	}
}

func TestExtractTextRejectsBinaryContentTypes(t *testing.T) {
	for _, contentType := range []string{"application/pdf", "image/png", "application/octet-stream", "application/zip"} {
		_, _, err := extractText(contentType, []byte{0x00, 0x01, 0x02})
		if err == nil {
			t.Fatalf("content type %q was accepted", contentType)
		}
		var failure toolFailure
		if !errors.As(err, &failure) {
			t.Fatalf("content type %q produced a non-explainable error: %v", contentType, err)
		}
	}
}

func TestExtractTextTreatsMissingContentTypeAsText(t *testing.T) {
	text, _, err := extractText("", []byte("plain body"))
	if err != nil {
		t.Fatalf("missing content type rejected: %v", err)
	}
	if text != "plain body" {
		t.Fatalf("text = %q", text)
	}
}

func TestExtractTextDecodesGBKFromHeaderCharset(t *testing.T) {
	encoded, err := encodeGBK("中文标题与正文")
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	text, _, err := extractText("text/plain; charset=gbk", encoded)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if text != "中文标题与正文" {
		t.Fatalf("decoded text = %q", text)
	}
}

func TestExtractTextSniffsMetaCharset(t *testing.T) {
	encoded, err := encodeGBK(`<html><head><meta charset="gb2312"></head><body>站点正文</body></html>`)
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	text, _, err := extractText("text/html", encoded)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if !strings.Contains(text, "站点正文") {
		t.Fatalf("meta charset was not honoured: %q", text)
	}
}

func TestExtractTextCollapsesWhitespace(t *testing.T) {
	body := []byte("<p>a    b</p>\n\n\n\n<p>c\u00a0\u00a0d</p>")
	text, _, err := extractText("text/html", body)
	if err != nil {
		t.Fatalf("extractText: %v", err)
	}
	if strings.Contains(text, "  ") || strings.Contains(text, "\n\n\n") {
		t.Fatalf("whitespace was not collapsed: %q", text)
	}
	if !strings.Contains(text, "a b") || !strings.Contains(text, "c d") {
		t.Fatalf("collapsed text lost content: %q", text)
	}
}

func TestNormalizeMediaTypeDropsParameters(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=UTF-8": "text/html",
		"  Application/JSON  ":     "application/json",
		"text/plain;charset=utf-8": "text/plain",
		"":                         "",
	}
	for input, want := range cases {
		if got := normalizeMediaType(input); got != want {
			t.Errorf("normalizeMediaType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncateUTF8KeepsRuneBoundaries(t *testing.T) {
	value := "中文中文中文"
	for limit := 0; limit <= len(value); limit++ {
		truncated := truncateUTF8(value, limit)
		if len(truncated) > limit {
			t.Fatalf("limit %d produced %d bytes", limit, len(truncated))
		}
		if strings.ContainsRune(truncated, '\uFFFD') {
			t.Fatalf("limit %d produced a replacement rune: %q", limit, truncated)
		}
		for _, char := range truncated {
			if char == '\uFFFD' {
				t.Fatalf("limit %d broke a rune", limit)
			}
		}
	}
}

func encodeGBK(value string) ([]byte, error) {
	encoded, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(value))
	return encoded, err
}
