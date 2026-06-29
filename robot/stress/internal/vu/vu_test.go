package vu

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isShutdownCanceled 只把 context.Canceled / gRPC codes.Canceled 当成关停排空中断;
// DeadlineExceeded(真实超时)与其它后端错误码仍算 error,不得被误判为 canceled。
func TestIsShutdownCanceled(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context.Canceled", context.Canceled, true},
		{"wrapped context.Canceled", fmt.Errorf("rpc 失败: %w", context.Canceled), true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"grpc Canceled", status.Error(codes.Canceled, "client canceled"), true},
		{"grpc DeadlineExceeded", status.Error(codes.DeadlineExceeded, "timeout"), false},
		{"grpc Unavailable", status.Error(codes.Unavailable, "down"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShutdownCanceled(tc.err); got != tc.want {
				t.Fatalf("isShutdownCanceled(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
