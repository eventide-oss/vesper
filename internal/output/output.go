// Package output implements the --output flag shared by the vesperctl
// commands, and writes values as JSON or YAML.
//
// The package knows nothing about Vesper resources: commands build the value
// to print and keep their own table layout.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

// Format is an output format. It implements pflag.Value, so an invalid value
// is rejected while the flags are parsed, before the command runs.
type Format string

// The supported formats. Table is the default, meant for humans.
const (
	Table Format = "table"
	JSON  Format = "json"
	YAML  Format = "yaml"
)

// formats lists the supported formats in the order shown to users.
var formats = []Format{Table, JSON, YAML}

// String returns the current format. pflag shows it as the default in --help.
func (f *Format) String() string { return string(*f) }

// Set parses s. pflag calls it with the value given on the command line.
func (f *Format) Set(s string) error {
	for _, valid := range formats {
		if Format(s) == valid {
			*f = valid
			return nil
		}
	}
	return fmt.Errorf("valid formats are %s", validFormats())
}

// Type names the value in --help.
func (f *Format) Type() string { return "format" }

func validFormats() string {
	names := make([]string, len(formats))
	for i, f := range formats {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

// AddFlag registers -o/--output on fs, stores the chosen format in f and
// sets it to Table until a value is given.
func AddFlag(fs *pflag.FlagSet, f *Format) {
	*f = Table
	fs.VarP(f, "output", "o", "output format: "+validFormats())
}

// Write writes v to w in format, which must be JSON or YAML: the table layout
// belongs to each command.
//
// The field names come from the json struct tags of v, for both formats.
func Write(w io.Writer, format Format, v any) error {
	switch format {
	case JSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		// Keep characters such as & in URLs readable instead of &.
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	case YAML:
		// sigs.k8s.io/yaml converts the JSON encoding to YAML, as kubectl does.
		out, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode YAML: %w", err)
		}
		_, err = w.Write(out)
		return err
	default:
		return fmt.Errorf("output.Write does not support format %q", format)
	}
}
