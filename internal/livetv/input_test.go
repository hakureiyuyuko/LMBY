package livetv

import (
	"reflect"
	"testing"
)

func TestInputArgs(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		headers string
		want    []string
	}{
		{
			name: "rtsp 走 tcp 并给读超时",
			url:  "rtsp://1.2.3.4:554/live/1",
			want: []string{"-rtsp_transport", "tcp", "-timeout", "15000000"},
		},
		{
			name:    "rtsp 带自定义请求头",
			url:     "rtsp://1.2.3.4:554/live/1",
			headers: "User-Agent=OkHttp&Referer=http://x/",
			want:    []string{"-rtsp_transport", "tcp", "-timeout", "15000000", "-headers", "User-Agent=OkHttp&Referer=http://x/"},
		},
		{
			name: "rtsps 也算 rtsp",
			url:  "rtsps://1.2.3.4/live",
			want: []string{"-rtsp_transport", "tcp", "-timeout", "15000000"},
		},
		{
			name: "rtmp 声明是直播流",
			url:  "rtmp://1.2.3.4/app/stream",
			want: []string{"-rtmp_live", "live"},
		},
		{
			name: "http/hls 不需要额外参数",
			url:  "http://1.2.3.4:8080/live/index.m3u8",
			want: nil,
		},
		{
			name:    "http 也能带请求头",
			url:     "http://1.2.3.4/x.m3u8",
			headers: "Referer=http://1.2.3.4/",
			want:    []string{"-headers", "Referer=http://1.2.3.4/"},
		},
		{
			name:    "空白请求头不算",
			url:     "http://1.2.3.4/x.m3u8",
			headers: "   ",
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InputArgs(c.url, c.headers)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("InputArgs(%q, %q) = %v，期望 %v", c.url, c.headers, got, c.want)
			}
		})
	}
}
