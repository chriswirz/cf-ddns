package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Possible is one record discovered in the account, written to the
// "possible_records" section when "records" is empty. The first five fields
// are exactly the shape of a "records" entry, so an entry can be moved across
// verbatim; the remaining fields are context and are ignored if left behind.
type Possible struct {
	Zone    string `json:"zone"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`

	// Content is the record's current value, so you can tell which of several
	// A records is the one pointing at this site.
	Content string `json:"content,omitempty"`
	// Comment carries the record's Cloudflare comment, when it has one.
	Comment string `json:"comment,omitempty"`
}

// PossibleHelp is written alongside the section so the file explains itself.
const PossibleHelp = "Discovered because \"records\" was empty. " +
	"Move the entries you want kept up to date into \"records\" and re-run. " +
	"Only the zone, name, type, ttl and proxied fields are used."

// WritePossibleRecords replaces the "possible_records" section of the config
// file at path with recs.
//
// The file is rewritten key by key in its original order so hand-written
// comments (the "// ..." keys) and formatting survive. The write goes to a
// temporary file in the same directory and is then renamed over the original,
// so an interrupted run cannot leave a truncated config behind.
func WritePossibleRecords(path string, recs []Possible) error {
	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keys, err := topLevelKeys(orig)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(orig, &raw); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	help, err := json.Marshal(PossibleHelp)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(recs, "  ", "  ")
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		body = []byte("[]")
	}
	const helpKey = "// possible_records"
	raw[helpKey] = help
	raw["possible_records"] = body
	// Append the two keys only if the file did not already carry them, so a
	// rewrite does not keep moving them around.
	for _, k := range []string{helpKey, "possible_records"} {
		if !contains(keys, k) {
			keys = append(keys, k)
		}
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		name, err := json.Marshal(k)
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "  %s: %s", name, raw[k])
		if i < len(keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")

	if !json.Valid(buf.Bytes()) {
		return fmt.Errorf("refusing to write %s: rewritten config is not valid JSON", path)
	}
	return writeAtomic(path, buf.Bytes())
}

// topLevelKeys returns the object's keys in the order they appear in the file,
// which is what json.Unmarshal into a map throws away.
func topLevelKeys(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("config must be a JSON object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v where a key was expected", tok)
		}
		keys = append(keys, key)
		// Consume the value, whatever shape it is.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// writeAtomic writes data to path via a temporary file in the same directory,
// copying the original file's permissions so a 0600 config stays private.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// A no-op once the rename below succeeds, and nothing useful to do if it
	// fails: the file is in a temp name that no one reads.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	// Windows will not rename onto an existing file, so clear the way first.
	// The temp file still holds the new content if this is interrupted.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpName, path)
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
