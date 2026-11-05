package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestAddFlag(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Format
		wantErr string
	}{
		{name: "default", args: nil, want: Table},
		{name: "table", args: []string{"-o", "table"}, want: Table},
		{name: "json", args: []string{"-o", "json"}, want: JSON},
		{name: "yaml, long flag", args: []string{"--output=yaml"}, want: YAML},
		{
			name:    "unknown format",
			args:    []string{"-o", "xml"},
			wantErr: `invalid argument "xml" for "-o, --output" flag: valid formats are table, json, yaml`,
		},
		{
			name:    "formats are case-sensitive",
			args:    []string{"-o", "JSON"},
			wantErr: "valid formats are table, json, yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			var f Format
			AddFlag(fs, &f)

			err := fs.Parse(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if f != tt.want {
				t.Errorf("format = %q, want %q", f, tt.want)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	type item struct {
		Name string `json:"name"`
		URL  string `json:"url,omitempty"`
	}
	v := struct {
		Items []item `json:"items"`
	}{Items: []item{{Name: "pr-1", URL: "https://example.com/?a=1&b=2"}, {Name: "pr-2"}}}

	tests := []struct {
		format Format
		want   string
	}{
		{
			format: JSON,
			want: `{
  "items": [
    {
      "name": "pr-1",
      "url": "https://example.com/?a=1&b=2"
    },
    {
      "name": "pr-2"
    }
  ]
}
`,
		},
		{
			format: YAML,
			want: `items:
- name: pr-1
  url: https://example.com/?a=1&b=2
- name: pr-2
`,
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			var out bytes.Buffer
			if err := Write(&out, tt.format, v); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if out.String() != tt.want {
				t.Errorf("output =\n%s\nwant\n%s", out.String(), tt.want)
			}
		})
	}
}

func TestWriteRejectsTable(t *testing.T) {
	if err := Write(&bytes.Buffer{}, Table, struct{}{}); err == nil {
		t.Error("Write(Table) returned no error")
	}
}
