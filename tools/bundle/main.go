// Command bundle assembles bqui.app from a darwin binary. It runs on any OS,
// so the app can be packaged on Linux and copied to a Mac.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

const (
	dirPerm  = 0o755
	exePerm  = 0o755
	filePerm = 0o644
)

var infoPlist = template.Must(template.New("Info.plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>en</string>
	<key>CFBundleDisplayName</key><string>{{.Name}}</string>
	<key>CFBundleExecutable</key><string>{{.Exe}}</string>
	<key>CFBundleIdentifier</key><string>{{.ID}}</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>CFBundleName</key><string>{{.Name}}</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>{{.Version}}</string>
	<key>CFBundleVersion</key><string>{{.Version}}</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
	<key>LSMinimumSystemVersion</key><string>{{.MinOS}}</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSPrincipalClass</key><string>NSApplication</string>
	<key>NSSupportsAutomaticTermination</key><false/>
	<key>NSSupportsSuddenTermination</key><false/>
</dict>
</plist>
`))

type bundleInfo struct {
	Name, Exe, ID, Version, MinOS string
}

func main() {
	bin := flag.String("bin", "", "path to the darwin bqui executable")
	out := flag.String("o", "dist/bqui.app", "output .app directory")
	notices := flag.String("notices", "THIRD_PARTY_NOTICES.md", "third-party notices copied into Contents/Resources ('' to skip)")
	info := bundleInfo{Name: "bqui", Exe: "bqui"}
	flag.StringVar(&info.ID, "id", "io.github.paulourio.bqui", "CFBundleIdentifier")
	flag.StringVar(&info.Version, "version", "0.0.0", "bundle version")
	flag.StringVar(&info.MinOS, "min-os", "26.0", "LSMinimumSystemVersion")
	flag.Parse()

	if *bin == "" {
		fmt.Fprintln(os.Stderr, "bundle: -bin is required")
		os.Exit(2)
	}

	if err := build(*bin, *out, *notices, info); err != nil {
		fmt.Fprintln(os.Stderr, "bundle:", err)
		os.Exit(1)
	}
}

func build(bin, out, notices string, info bundleInfo) error {
	exe, err := os.ReadFile(bin)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(out); err != nil {
		return err
	}

	contents := filepath.Join(out, "Contents")
	for _, d := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(contents, d), dirPerm); err != nil {
			return err
		}
	}

	var plist bytes.Buffer
	if err := infoPlist.Execute(&plist, info); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), plist.Bytes(), filePerm); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), filePerm); err != nil {
		return err
	}

	if notices != "" {
		text, err := os.ReadFile(notices)
		if err != nil {
			return err
		}

		//nolint:gosec // G703: build tool; paths come from the developer's own flags.
		if err := os.WriteFile(filepath.Join(contents, "Resources", filepath.Base(notices)), text, filePerm); err != nil {
			return err
		}
	}

	//nolint:gosec // G703: build tool; the output path comes from the developer's own -o flag.
	return os.WriteFile(filepath.Join(contents, "MacOS", info.Exe), exe, exePerm)
}
