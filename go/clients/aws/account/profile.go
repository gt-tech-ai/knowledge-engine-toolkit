package account

import (
	"os"
	"path/filepath"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Profile is one SSO profile a sign-in flow writes to the shared AWS config: an
// `[sso-session]` stanza and the `[profile]` stanza that uses it.
type Profile struct {
	// Name is the profile name (the `[profile <Name>]` header).
	Name string

	// SSOSession names the `[sso-session]` stanza.
	SSOSession string

	// SSOStartURL is the IAM Identity Center start URL.
	SSOStartURL string

	// SSORegion is the Identity Center region.
	SSORegion string

	// AccountID is the account the profile signs in to.
	AccountID string

	// RoleName is the permission set the profile assumes.
	RoleName string

	// Region is the profile's default region.
	Region string
}

// ConfigPath returns the shared AWS config path: AWS_CONFIG_FILE when set, else
// ~/.aws/config, so the writer and every reader agree on one file.
func ConfigPath(getenv func(string) string) (string, error) {
	if override := getenv("AWS_CONFIG_FILE"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			"resolve home dir for ~/.aws/config",
		)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

// UpsertProfile writes p's stanzas inside the block owner manages in the AWS config at
// path (fenced by `# >>> <owner> managed` and `# <<< <owner> managed`), preserving
// every byte outside it. Stanzas are keyed by header, so a re-run replaces them in place
// and leaves the block's other profiles alone. The file is created 0600 (its dir 0700)
// and written atomically. A same-named stanza outside the block is CodeInvalidInput:
// the AWS CLI would merge it over the managed one.
func UpsertProfile(path, owner string, p Profile) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return apperr.Wrap(err, apperr.CodeInternal, "read aws config "+path)
	}
	begin, end := "# >>> "+owner+" managed", "# <<< "+owner+" managed"
	pre, inner, post := splitManaged(string(existing), begin, end)
	sessionHeader, profileHeader := "sso-session "+p.SSOSession, "profile "+p.Name
	if err := refuseUnmanaged(
		path,
		owner,
		pre+post,
		sessionHeader,
		profileHeader,
	); err != nil {
		return err
	}

	sections := parseSections(inner)
	sections = upsertSection(sections, sessionHeader, []string{
		"sso_start_url = " + p.SSOStartURL,
		"sso_region = " + p.SSORegion,
		"sso_registration_scopes = sso:account:access",
	})
	sections = upsertSection(sections, profileHeader, []string{
		"sso_session = " + p.SSOSession,
		"sso_account_id = " + p.AccountID,
		"sso_role_name = " + p.RoleName,
		"region = " + p.Region,
	})

	var b strings.Builder
	b.WriteString(pre)
	b.WriteString(begin + "\n")
	b.WriteString(renderSections(sections))
	b.WriteString(end + "\n")
	b.WriteString(post)
	return writeFileAtomic(path, []byte(b.String()))
}

// refuseUnmanaged fails when the operator's own (unfenced) text defines one of headers.
func refuseUnmanaged(path, owner, unmanaged string, headers ...string) error {
	for line := range strings.SplitSeq(unmanaged, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, h := range headers {
			if trimmed == "["+h+"]" {
				return apperr.New(apperr.CodeInvalidInput, path+" defines ["+h+
					"] outside the "+owner+"-managed block; the aws CLI would merge it "+
					"over the managed stanza — delete it and sign in again")
			}
		}
	}
	return nil
}

// splitManaged partitions content into the text before the managed block, the text
// between the fences and the text after. With no fence the whole file is the prefix
// (newline-terminated, plus a blank line) so the block is appended at the end; a begin
// with no end treats the remainder as the block's text and re-fences it.
func splitManaged(content, begin, end string) (pre, inner, post string) {
	at := strings.Index(content, begin+"\n")
	if at == -1 {
		pre = content
		if pre != "" && !strings.HasSuffix(pre, "\n") {
			pre += "\n"
		}
		if pre != "" {
			pre += "\n"
		}
		return pre, "", ""
	}
	pre = content[:at]
	rest := content[at+len(begin)+1:]
	stop := strings.Index(rest, end+"\n")
	if stop == -1 {
		return pre, rest, ""
	}
	return pre, rest[:stop], rest[stop+len(end)+1:]
}

// iniSection is one `[header]` stanza and its body lines, in file order.
type iniSection struct {
	// header is the stanza name without its brackets.
	header string

	// body is the stanza's non-blank lines.
	body []string
}

// parseSections splits the managed block into stanzas, dropping blank lines and any
// line before the first header (the writer owns every byte between the fences).
func parseSections(inner string) []iniSection {
	var sections []iniSection
	for line := range strings.SplitSeq(inner, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
			sections = append(sections, iniSection{header: trimmed[1 : len(trimmed)-1]})
		case trimmed == "" || len(sections) == 0:
		default:
			last := &sections[len(sections)-1]
			last.body = append(last.body, trimmed)
		}
	}
	return sections
}

// upsertSection replaces header's body, or appends the stanza when absent.
func upsertSection(sections []iniSection, header string, body []string) []iniSection {
	for i := range sections {
		if sections[i].header == header {
			sections[i].body = body
			return sections
		}
	}
	return append(sections, iniSection{header: header, body: body})
}

// renderSections serializes stanzas, one newline-terminated line each.
func renderSections(sections []iniSection) string {
	var b strings.Builder
	for _, s := range sections {
		b.WriteString("[" + s.header + "]\n")
		for _, line := range s.body {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// writeFileAtomic writes data through a temp file and a rename, so a crash never
// leaves a half-written config; the dir is created 0700 and the file 0600.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create dir "+dir)
	}
	tmp, err := os.CreateTemp(dir, ".aws-config-*")
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create temp aws config")
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return apperr.Wrap(err, apperr.CodeInternal, "chmod temp aws config")
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return apperr.Wrap(err, apperr.CodeInternal, "write temp aws config")
	}
	if err := tmp.Close(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "close temp aws config")
	}
	if err := os.Rename(name, path); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "rename temp aws config into place")
	}
	return nil
}
