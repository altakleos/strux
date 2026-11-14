package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/altakleos/strux/format"
)

// kernelPackage is the package that interpretation revision 1 interprets as its kernel: the one
// whose definitions verification reads, and below every other package.
const kernelPackage = "kernel"

func newDef(out *output) *cobra.Command {
	def := &cobra.Command{
		Use:   "def [command]",
		Short: "Work with definitions files.",
		Args:  noArgs,
		RunE: out.runs(func(c *cobra.Command, _ []string) (any, error) {
			return describe(c), nil
		}),
	}
	def.AddCommand(newDefCheck(out))
	return def
}

func newDefCheck(out *output) *cobra.Command {
	return &cobra.Command{
		Use:   "check <file>...",
		Short: "Check definitions files against definition format 1.",
		Args:  atLeastOne,
		RunE: out.runs(func(_ *cobra.Command, files []string) (any, error) {
			return checkDefinitions(files), nil
		}),
	}
}

// atLeastOne accepts one argument or more.
func atLeastOne(c *cobra.Command, args []string) error {
	if len(args) == 0 {
		return usageError{c, fmt.Errorf("%s takes one file or more", c.CommandPath())}
	}
	return nil
}

// definitionsCheck is what def check found: each package it read, in the order of the files, and
// every problem. The files are checked together, so a package may depend on another one that the
// command line names.
type definitionsCheck struct {
	Packages []checkedPackage `json:"packages"`
	Problems []string         `json:"problems"`
}

// checkedPackage identifies a package read: its name and revision, and the digest of the canonical
// form of its definitions, by which facts and configurations name it.
type checkedPackage struct {
	Package  string `json:"package"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	File     string `json:"file"`
}

// failure makes a check that found problems fail, once its result is written.
func (d definitionsCheck) failure() error {
	if len(d.Problems) == 0 {
		return nil
	}
	if len(d.Problems) == 1 {
		return fmt.Errorf("the definitions files have 1 problem")
	}
	return fmt.Errorf("the definitions files have %d problems", len(d.Problems))
}

func checkDefinitions(files []string) definitionsCheck {
	result := definitionsCheck{Packages: []checkedPackage{}, Problems: []string{}}
	problem := func(file, format string, args ...any) {
		result.Problems = append(result.Problems, file+": "+fmt.Sprintf(format, args...))
	}
	pkgs := map[string]*format.Package{}
	from := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file) //nolint:gosec // def check reads the files its command line names
		if err != nil {
			problem(file, "cannot be read: %v", err)
			continue
		}
		content, err := format.Parse(data)
		if err != nil {
			problem(file, "%v", err)
			continue
		}
		top, _ := content.(map[string]any)
		name, _ := top["package"].(string)
		switch {
		case name == "":
			problem(file, "package: names no package")
			continue
		case from[name] != "":
			problem(file, "package: %s is defined in %s as well", name, from[name])
			continue
		}
		p := format.Build(name, file, content)
		pkgs[name], from[name] = p, file
		result.Packages = append(result.Packages, checkedPackage{Package: name, Revision: p.Revision, Digest: p.Digest, File: file})
	}
	result.Problems = append(result.Problems, format.Check(pkgs, format.Kernel(kernelPackage))...)
	return result
}
