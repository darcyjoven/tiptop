package deploycli

import (
	"reflect"
	"testing"
)

func TestHasFlagsAfterOperand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"只有文件", []string{"a.4gl"}, false},
		{"flag 在前", []string{"-dry-run", "a.4gl"}, false},
		{"flag 在后", []string{"a.4gl", "-dry-run"}, true},
		{"带值 flag 无文件", []string{"-target", "test"}, false},
		{"带值 flag 有文件", []string{"-target", "test", "a.4gl"}, false},
		{"带值 flag 后有 flag", []string{"-target", "test", "a.4gl", "-dry-run"}, true},
		{"flag=value 形式", []string{"-target=test", "a.4gl"}, false},
		{"空", nil, false},
	}
	for _, tc := range tests {
		if got := hasFlagsAfterOperand(tc.args); got != tc.want {
			t.Errorf("%s: hasFlagsAfterOperand(%v) = %v，期望 %v",
				tc.name, tc.args, got, tc.want)
		}
	}
}

func TestReorderArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantFlags []string
		wantFiles []string
	}{
		{
			name:      "flag 在前",
			args:      []string{"-dry-run", "a.4gl", "b.per"},
			wantFlags: []string{"-dry-run"},
			wantFiles: []string{"a.4gl", "b.per"},
		},
		{
			name:      "带值 flag",
			args:      []string{"-target", "test", "a.4gl"},
			wantFlags: []string{"-target", "test"},
			wantFiles: []string{"a.4gl"},
		},
		{
			name:      "文件与 flag 混排",
			args:      []string{"a.4gl", "-dry-run", "b.per"},
			wantFlags: []string{"-dry-run"},
			wantFiles: []string{"a.4gl", "b.per"},
		},
		{
			name:      "flag=value",
			args:      []string{"a.4gl", "-target=test"},
			wantFlags: []string{"-target=test"},
			wantFiles: []string{"a.4gl"},
		},
	}
	for _, tc := range tests {
		flags, files := reorderArgs(tc.args)
		if !reflect.DeepEqual(flags, tc.wantFlags) {
			t.Errorf("%s: flags = %v，期望 %v", tc.name, flags, tc.wantFlags)
		}
		if !reflect.DeepEqual(files, tc.wantFiles) {
			t.Errorf("%s: files = %v，期望 %v", tc.name, files, tc.wantFiles)
		}
	}
}
