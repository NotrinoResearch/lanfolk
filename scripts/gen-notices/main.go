// Command gen-notices writes THIRD_PARTY_NOTICES.md: the name, version,
// license and full license text of every non-standard-library Go module that
// ends up in a Lanfolk binary, plus fixed sections for the Wintun DLL, the Go
// standard library and the WireGuard trademark.
//
// It uses only the standard library and the go command. Run it from the
// repository root:
//
//	go run ./scripts/gen-notices            (writes THIRD_PARTY_NOTICES.md)
//	go run ./scripts/gen-notices -o -       (writes to standard output)
//
// The set of modules comes from "go list -deps" over the release binaries.
// Packages that do not exist yet (for example ./ui) are skipped, so the tool
// works before and after they are added.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// targets are the release binaries. The relay is built for Linux, the others
// for Windows; ui additionally needs the Wails build tags. natprobe and
// lanprobe ship in the release archive. ./internal/... is the library code
// the service is built from; it keeps the notices complete while
// ./cmd/lanfolk-service does not exist yet, and costs nothing afterwards.
var targets = []target{
	{goos: "windows", tags: "desktop,production", pkgs: []string{
		"./cmd/lanfolk-service", "./cmd/lanfolk-cli", "./cmd/natprobe", "./cmd/lanprobe", "./ui", "./internal/..."}},
	{goos: "linux", pkgs: []string{"./cmd/lanfolk-relay"}},
}

type target struct {
	goos string
	tags string
	pkgs []string
}

// module is one third-party module.
type module struct {
	Path    string
	Version string
	Dir     string
	Files   []licenseFile
}

type licenseFile struct {
	Name string
	Text string
}

func main() {
	out := flag.String("o", "THIRD_PARTY_NOTICES.md", "output file, or - for standard output")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gen-notices:", err)
		os.Exit(1)
	}
}

func run(outPath string) error {
	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("run this from the repository root (go.mod not found)")
	}
	mods := map[string]*module{}
	for _, t := range targets {
		pkgs := existing(t.pkgs)
		if len(pkgs) == 0 {
			continue
		}
		if err := collect(t, pkgs, mods); err != nil {
			return err
		}
	}
	if len(mods) == 0 {
		return fmt.Errorf("no third-party modules found")
	}
	list := make([]*module, 0, len(mods))
	for _, m := range mods {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })

	var missing []string
	for _, m := range list {
		m.Files = findLicenses(m.Dir)
		if len(m.Files) == 0 {
			missing = append(missing, m.Path+"@"+m.Version)
		}
	}
	goroot, goVersion, err := goEnv()
	if err != nil {
		return err
	}
	goLicense, _ := os.ReadFile(filepath.Join(goroot, "LICENSE"))

	var b bytes.Buffer
	render(&b, list, string(goLicense), goVersion)
	if outPath == "-" {
		_, err = os.Stdout.Write(b.Bytes())
	} else {
		err = os.WriteFile(outPath, b.Bytes(), 0o644)
	}
	if err != nil {
		return err
	}
	for _, m := range missing {
		fmt.Fprintf(os.Stderr, "warning: no license file found for %s\n", m)
	}
	fmt.Fprintf(os.Stderr, "gen-notices: %d modules\n", len(list))
	return nil
}

// existing keeps the package patterns whose directory exists and contains Go
// files.
func existing(pkgs []string) []string {
	var out []string
	for _, p := range pkgs {
		dir := strings.TrimPrefix(p, "./")
		recursive := strings.HasSuffix(dir, "/...")
		dir = strings.TrimSuffix(dir, "/...")
		if hasGoFiles(dir, recursive) {
			out = append(out, p)
		}
	}
	return out
}

// hasGoFiles reports whether dir (or, if recursive, any directory below it)
// contains a .go file.
func hasGoFiles(dir string, recursive bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
		if recursive && e.IsDir() && hasGoFiles(filepath.Join(dir, e.Name()), true) {
			return true
		}
	}
	return false
}

// collect runs go list -deps for one target and adds its non-standard-library,
// non-main modules to mods.
func collect(t target, pkgs []string, mods map[string]*module) error {
	args := []string{"list", "-deps"}
	if t.tags != "" {
		args = append(args, "-tags", t.tags)
	}
	args = append(args, "-f", `{{if and .Module (not .Module.Main)}}{{.Module.Path}}{{"\t"}}{{.Module.Version}}{{"\t"}}{{.Module.Dir}}{{end}}`)
	args = append(args, pkgs...)
	cmd := exec.Command("go", args...)
	// CGO_ENABLED=1 selects the superset of files, so no dependency is missed.
	// The default -mod=readonly keeps go.mod and go.sum untouched.
	cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH=amd64", "CGO_ENABLED=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list for %s: %v\n%s", strings.Join(pkgs, " "), err, stderr.String())
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(strings.TrimSpace(sc.Text()), "\t")
		if len(f) != 3 || f[0] == "" {
			continue
		}
		if _, ok := mods[f[0]]; !ok {
			mods[f[0]] = &module{Path: f[0], Version: f[1], Dir: f[2]}
		}
	}
	return sc.Err()
}

func goEnv() (goroot, version string, err error) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", "", err
	}
	goroot = strings.TrimSpace(string(out))
	out, err = exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return "", "", err
	}
	return goroot, strings.TrimSpace(string(out)), nil
}

// licenseNames are the base names (without extension, lowercase) that count
// as license files.
var licenseNames = map[string]bool{
	"license": true, "licence": true, "copying": true, "notice": true,
	"unlicense": true, "patents": true, "copyright": true,
}

// findLicenses returns the license files in the module root. For a module
// that lives in a subdirectory of another module and has none of its own, it
// looks in parent directories as well, up to the module cache boundary.
func findLicenses(dir string) []licenseFile {
	if dir == "" {
		return nil
	}
	files := licensesIn(dir)
	if len(files) > 0 {
		return files
	}
	// Nested module without its own license: the repository root usually has
	// one. Walk up at most three levels, but only through directories whose
	// name does not look like another module version.
	d := dir
	for i := 0; i < 3; i++ {
		d = filepath.Dir(d)
		if strings.Contains(filepath.Base(d), "@") {
			break
		}
		if files = licensesIn(d); len(files) > 0 {
			return files
		}
	}
	// Last resort: a sibling directory named like the parent module (module
	// cache layout: .../missinggo@v1.3.0 next to .../missinggo/perf@v1.0.0).
	parent := filepath.Dir(dir)
	matches, _ := filepath.Glob(parent + "@*")
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		if files = licensesIn(matches[i]); len(files) > 0 {
			return files
		}
	}
	return nil
}

func licensesIn(dir string) []licenseFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []licenseFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		if i := strings.IndexAny(base, "-_."); i > 0 {
			base = base[:i]
		}
		if !licenseNames[base] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(bytes.TrimSpace(data)) == 0 || len(data) > 512*1024 {
			continue
		}
		out = append(out, licenseFile{Name: name, Text: normalize(string(data))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// normalize converts line endings to LF and trims trailing white space.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// guessLicense names the license of a set of files from characteristic
// phrases. It is a hint for readers; the full text is always included.
func guessLicense(files []licenseFile) string {
	var names []string
	seen := map[string]bool{}
	for _, f := range files {
		n := classify(f.Name, f.Text)
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "Unknown (see text)"
	}
	return strings.Join(names, " AND ")
}

func classify(file, text string) string {
	t := strings.ToLower(text)
	one := strings.Join(strings.Fields(t), " ")
	switch {
	case strings.EqualFold(strings.TrimSuffix(file, filepath.Ext(file)), "patents"):
		return "" // additional patent grant, not a license on its own
	case strings.Contains(one, "apache license") && strings.Contains(one, "version 2.0"):
		return "Apache-2.0"
	case strings.Contains(one, "mozilla public license") && strings.Contains(one, "2.0"):
		return "MPL-2.0"
	case strings.Contains(one, "gnu lesser general public license"):
		return "LGPL (see text)"
	case strings.Contains(one, "gnu general public license"):
		return "GPL (see text)"
	case strings.Contains(one, "permission to use, copy, modify, and/or distribute this software for any purpose with or without fee"),
		strings.Contains(one, "permission to use, copy, modify, and distribute this software for any purpose with or without fee"):
		return "ISC"
	case strings.Contains(one, "redistribution and use in source and binary forms"):
		if strings.Contains(one, "neither the name") || strings.Contains(one, "the names of its contributors") {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	case strings.Contains(one, "permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(one, "this is free and unencumbered software released into the public domain"):
		return "Unlicense"
	case strings.Contains(one, "creative commons") && strings.Contains(one, "cc0"):
		return "CC0-1.0"
	case strings.Contains(one, "notice") && len(one) < 600:
		return ""
	}
	return "Unknown (see text)"
}

// fence returns a code fence longer than any backtick run in text.
func fence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := 3
	if longest >= n {
		n = longest + 1
	}
	return strings.Repeat("`", n)
}

func writeText(b *bytes.Buffer, text string) {
	f := fence(text)
	b.WriteString(f + "text\n")
	b.WriteString(text)
	b.WriteString("\n" + f + "\n")
}

func anchor(path, version string) string {
	s := strings.ToLower(path + "-" + version)
	var o strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			o.WriteRune(r)
		case r == ' ' || r == '/' || r == '.' || r == '_' || r == '@' || r == '+':
			o.WriteRune('-')
		}
	}
	return o.String()
}

func render(b *bytes.Buffer, mods []*module, goLicense, goVersion string) {
	b.WriteString("# Third-party notices\n\n")
	b.WriteString("Lanfolk is licensed under the Apache License 2.0 (see `LICENSE`). It includes\n")
	b.WriteString("or links the third-party components listed here. This file is generated by\n")
	b.WriteString("`go run ./scripts/gen-notices`; do not edit it by hand.\n\n")
	b.WriteString("Contents: [Go modules](#go-modules), [Go standard library](#go-standard-library),\n")
	b.WriteString("[Wintun](#wintun), [WireGuard trademark](#wireguard-trademark).\n\n")

	b.WriteString("## Go modules\n\n")
	b.WriteString("These modules are compiled into `lanfolk-service`, `lanfolk-cli`, `lanfolk-relay` or the\n")
	b.WriteString("desktop app (`go list -deps`). The license name is a guess from the license text;\n")
	b.WriteString("the full text below is what applies. Modules under MPL-2.0 are used unmodified;\n")
	b.WriteString("their source code is available from the module path shown (for example\n")
	b.WriteString("<https://github.com/anacrolix/torrent>) at the listed version.\n\n")
	b.WriteString("| Module | Version | License |\n| --- | --- | --- |\n")
	for _, m := range mods {
		fmt.Fprintf(b, "| `%s` | `%s` | %s |\n", m.Path, m.Version, guessLicense(m.Files))
	}
	b.WriteString("\n")
	for _, m := range mods {
		fmt.Fprintf(b, "### %s %s\n\n", m.Path, m.Version)
		fmt.Fprintf(b, "License: %s\n\n", guessLicense(m.Files))
		if len(m.Files) == 0 {
			b.WriteString("No license file was found in the module. See the module's repository.\n\n")
			continue
		}
		for _, f := range m.Files {
			if len(m.Files) > 1 {
				fmt.Fprintf(b, "File `%s`:\n\n", f.Name)
			}
			writeText(b, f.Text)
			b.WriteString("\n")
		}
	}

	b.WriteString("## Go standard library\n\n")
	fmt.Fprintf(b, "The binaries are built with the Go toolchain (%s) and include parts of the Go\n", goVersion)
	b.WriteString("standard library, which is licensed as follows (BSD-3-Clause).\n\n")
	if strings.TrimSpace(goLicense) != "" {
		writeText(b, normalize(goLicense))
	} else {
		b.WriteString("See <https://go.dev/LICENSE>.\n")
	}
	b.WriteString("\n")

	b.WriteString(wintunSection)
	b.WriteString("\n")
	b.WriteString(wireguardSection)
}

const wintunSection = `## Wintun

The Windows installer and release archive include **wintun.dll** (Wintun 0.14.1,
amd64), the layer 3 TUN driver library by WireGuard LLC, obtained from
<https://www.wintun.net/builds/wintun-0.14.1.zip>. Lanfolk loads it at run time
and uses it only through the documented API in ` + "`wintun.h`" + `.

Wintun's source code is published separately by its authors under its own
open-source licenses, but the **prebuilt, signed DLL is distributed under a
separate "Prebuilt Binaries License"**, reproduced below. Lanfolk therefore distributes only the unmodified, signed DLL from
wintun.net, never a rebuilt or modified one, and never Wintun source. The DLL is
distributed alongside software that uses it only via the permitted API, as the
license requires. Do not repackage it separately from Lanfolk.

` + "```text" + `
` + wintunLicense + `
` + "```" + `
`

const wireguardSection = `## WireGuard trademark

"WireGuard" is a registered trademark of Jason A. Donenfeld. Lanfolk is not
affiliated with, endorsed by or sponsored by the WireGuard project or WireGuard
LLC. The name is used only to describe compatibility: Lanfolk speaks the
WireGuard protocol through the wireguard-go library (MIT license, listed
above). The product name is Lanfolk.
`

// wintunLicense is the text of LICENSE.txt in wintun-0.14.1.zip (SHA-256 of the
// zip: 07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51).
const wintunLicense = `Prebuilt Binaries License
-------------------------

1. DEFINITIONS. "Software" means the precise contents of the "wintun.dll"
   files that are included in the .zip file that contains this document as
   downloaded from wintun.net/builds.

2. LICENSE GRANT. WireGuard LLC grants to you a non-exclusive and
   non-transferable right to use Software for lawful purposes under certain
   obligations and limited rights as set forth in this agreement.

3. RESTRICTIONS. Software is owned and copyrighted by WireGuard LLC. It is
   licensed, not sold. Title to Software and all associated intellectual
   property rights are retained by WireGuard. You must not:
   a. reverse engineer, decompile, disassemble, extract from, or otherwise
      modify the Software;
   b. modify or create derivative work based upon Software in whole or in
      parts, except insofar as only the API interfaces of the "wintun.h" file
      distributed alongside the Software (the "Permitted API") are used;
   c. remove any proprietary notices, labels, or copyrights from the Software;
   d. resell, redistribute, lease, rent, transfer, sublicense, or otherwise
      transfer rights of the Software without the prior written consent of
      WireGuard LLC, except insofar as the Software is distributed alongside
      other software that uses the Software only via the Permitted API;
   e. use the name of WireGuard LLC, the WireGuard project, the Wintun
      project, or the names of its contributors to endorse or promote products
      derived from the Software without specific prior written consent.

4. LIMITED WARRANTY. THE SOFTWARE IS PROVIDED "AS IS" AND WITHOUT WARRANTY OF
   ANY KIND. WIREGUARD LLC HEREBY EXCLUDES AND DISCLAIMS ALL IMPLIED OR
   STATUTORY WARRANTIES, INCLUDING ANY WARRANTIES OF MERCHANTABILITY, FITNESS
   FOR A PARTICULAR PURPOSE, QUALITY, NON-INFRINGEMENT, TITLE, RESULTS,
   EFFORTS, OR QUIET ENJOYMENT. THERE IS NO WARRANTY THAT THE PRODUCT WILL BE
   ERROR-FREE OR WILL FUNCTION WITHOUT INTERRUPTION. YOU ASSUME THE ENTIRE
   RISK FOR THE RESULTS OBTAINED USING THE PRODUCT. TO THE EXTENT THAT
   WIREGUARD LLC MAY NOT DISCLAIM ANY WARRANTY AS A MATTER OF APPLICABLE LAW,
   THE SCOPE AND DURATION OF SUCH WARRANTY WILL BE THE MINIMUM PERMITTED UNDER
   SUCH LAW. ALL EXPRESS OR IMPLIED CONDITIONS, REPRESENTATIONS AND
   WARRANTIES, INCLUDING ANY IMPLIED WARRANTY OF MERCHANTABILITY, FITNESS FOR
   A PARTICULAR PURPOSE OR NON-INFRINGEMENT ARE DISCLAIMED, EXCEPT TO THE
   EXTENT THAT THESE DISCLAIMERS ARE HELD TO BE LEGALLY INVALID.

5. LIMITATION OF LIABILITY. To the extent not prohibited by law, in no event
   WireGuard LLC or any third-party-developer will be liable for any lost
   revenue, profit or data or for special, indirect, consequential, incidental
   or punitive damages, however caused regardless of the theory of liability,
   arising out of or related to the use of or inability to use Software, even
   if WireGuard LLC has been advised of the possibility of such damages.
   Solely you are responsible for determining the appropriateness of using
   Software and accept full responsibility for all risks associated with its
   exercise of rights under this agreement, including but not limited to the
   risks and costs of program errors, compliance with applicable laws, damage
   to or loss of data, programs or equipment, and unavailability or
   interruption of operations. The foregoing limitations will apply even if
   the above stated warranty fails of its essential purpose. You acknowledge,
   that it is in the nature of software that software is complex and not
   completely free of errors. In no event shall WireGuard LLC or any
   third-party-developer be liable to you under any theory for any damages
   suffered by you or any user of Software or for any special, incidental,
   indirect, consequential or similar damages (including without limitation
   damages for loss of business profits, business interruption, loss of
   business information or any other pecuniary loss) arising out of the use or
   inability to use Software, even if WireGuard LLC has been advised of the
   possibility of such damages and regardless of the legal or quitable theory
   (contract, tort, or otherwise) upon which the claim is based.

6. TERMINATION. This agreement is affected until terminated. You may
   terminate this agreement at any time. This agreement will terminate
   immediately without notice from WireGuard LLC if you fail to comply with
   the terms and conditions of this agreement. Upon termination, you must
   delete Software and all copies of Software and cease all forms of
   distribution of Software.

7. SEVERABILITY. If any provision of this agreement is held to be
   unenforceable, this agreement will remain in effect with the provision
   omitted, unless omission would frustrate the intent of the parties, in
   which case this agreement will immediately terminate.

8. RESERVATION OF RIGHTS. All rights not expressly granted in this agreement
   are reserved by WireGuard LLC. For example, WireGuard LLC reserves the
   right at any time to cease development of Software, to alter distribution
   details, features, specifications, capabilities, functions, licensing
   terms, release dates, APIs, ABIs, general availability, or other
   characteristics of the Software.`
