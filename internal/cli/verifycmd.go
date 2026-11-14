package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/altakleos/strux/format"
	"github.com/altakleos/strux/verify"
)

func newVerify(out *output) *cobra.Command {
	var trustRoot, anchors, use, subject string
	var position int
	c := &cobra.Command{
		Use:   "verify <log-dir>",
		Short: "Verify a log, and a use relied on for a revision in it.",
		Args:  exactlyOne,
		RunE: out.runs(func(c *cobra.Command, args []string) (any, error) {
			if trustRoot == "" {
				return nil, usageError{c, errors.New("--trust-root names the file of the initial trust root")}
			}
			if (use == "") != (subject == "") {
				return nil, usageError{c, errors.New("--use and --subject go together")}
			}
			if c.Flags().Changed("position") && use == "" {
				return nil, usageError{c, errors.New("--position needs --use and --subject")}
			}
			in, l, err := verifyInputs(args[0], trustRoot, anchors)
			if err != nil {
				return nil, err
			}
			if use != "" {
				fact, digest, ok := strings.Cut(subject, ":sha256:")
				if !ok || !strings.HasPrefix(fact, "sha256:") {
					return nil, usageError{c, fmt.Errorf("--subject is the identity of a fact and the digest of its revision, as sha256:<hex>:sha256:<hex>")}
				}
				in.Reliance = &verify.Reliance{Use: use, Subject: format.Reference{Fact: fact, Digest: "sha256:" + digest}}
				if c.Flags().Changed("position") {
					in.Reliance.Position = &position
				}
			}
			res, err := verify.Verify(l, in)
			if err != nil {
				return nil, err
			}
			return verification{res}, nil
		}),
	}
	c.Flags().StringVar(&trustRoot, "trust-root", "", "The file that holds the initial trust root, as JSON or YAML.")
	c.Flags().StringVar(&anchors, "anchors", "", "The file of the anchors read, one to a line; the log directory's anchors.jsonl by default.")
	c.Flags().StringVar(&use, "use", "", "The use relied on, as package/definition.uses.name.")
	c.Flags().StringVar(&subject, "subject", "", "The revision the use is relied on for: the identity of the fact that recorded it and its digest.")
	c.Flags().IntVar(&position, "position", 0, "The position the use is relied on at; the number of entries read by default.")
	return c
}

// exactlyOne accepts exactly one argument.
func exactlyOne(c *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usageError{c, fmt.Errorf("%s takes one log directory", c.CommandPath())}
	}
	return nil
}

// verifyInputs reads the log in the interchange layout, the trust root and the anchors.
func verifyInputs(dir, trustRootFile, anchorsFile string) (verify.Inputs, *verify.Log, error) {
	l, err := verify.ReadLog(os.DirFS(dir))
	if err != nil {
		return verify.Inputs{}, nil, fmt.Errorf("the log: %w", err)
	}
	data, err := os.ReadFile(trustRootFile) //nolint:gosec // verify reads the trust root its command line names
	if err != nil {
		return verify.Inputs{}, nil, fmt.Errorf("the trust root: %w", err)
	}
	root, err := readValue(data)
	if err != nil {
		return verify.Inputs{}, nil, fmt.Errorf("the trust root: %w", err)
	}
	in := verify.Inputs{TrustRoot: root, Size: len(l.Records), Anchors: l.Anchors}
	if m, ok := root.(map[string]any); ok {
		in.Log, _ = m["log"].(string)
	}
	if anchorsFile != "" {
		data, err := os.ReadFile(filepath.Clean(anchorsFile))
		if err != nil {
			return verify.Inputs{}, nil, fmt.Errorf("the anchors: %w", err)
		}
		in.Anchors = format.Records(data)
	}
	return in, l, nil
}

// readValue reads a JSON or YAML file as the format reads values.
func readValue(data []byte) (any, error) {
	if v, err := format.ParseJSON(data); err == nil {
		return v, nil
	}
	return format.Parse(data)
}

// verification is verify's result, which fails the command when the use relied on does not hold.
type verification struct{ *verify.Result }

func (v verification) failure() error {
	if v.Reliance != nil && v.Reliance.Status != "holds" {
		return fmt.Errorf("the use relied on is %s", v.Reliance.Status)
	}
	return nil
}
