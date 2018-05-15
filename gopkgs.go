package gopkgs

import (
	"bufio"
	"errors"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/karrick/godirwalk"
)

// Pkg hold the information of the package.
type Pkg struct {
	Dir        string // directory containing package sources
	ImportPath string // import path of package in dir
	Name       string // package name
}

// Options for retrieve packages.
type Options struct {
	WorkDir  string // Will return importable package under WorkDir. Any vendor dependencies outside the WorkDir will be ignored.
	NoVendor bool   // Will not retrieve vendor dependencies, except inside WorkDir (if specified)
}

type goFile struct {
	path   string
	dir    string
	srcDir string
}

func readPackageName(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}

	defer func() {
		if err := f.Close(); err != nil {
			panic(err)
		}
	}()

	s := bufio.NewScanner(f)
	var inComment bool
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "/*") {
			inComment = true
		}

		if strings.HasSuffix(line, "*/") {
			inComment = false
		}

		if !inComment && strings.HasPrefix(line, "package") {
			line = strings.TrimSpace(line)
			ls := strings.Split(line, " ")
			if len(ls) < 2 {
				return "", errors.New("expect pattern 'package <name>':" + line)
			}
			return ls[1], nil
		}
	}

	return "", errors.New("cannot find package information")
}

// Packages available to import.
func Packages(opts Options) (map[string]Pkg, error) {
	var pkgsMu sync.Mutex
	pkgs := make(map[string]Pkg)

	workDir := opts.WorkDir
	if workDir != "" && !filepath.IsAbs(workDir) {
		wd, err := filepath.Abs(workDir)
		if err != nil {
			return nil, err
		}

		workDir = wd
	}

	goFileCh := make(chan goFile, 10000)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			for gf := range goFileCh {
				pkgName, err := readPackageName(gf.path)
				if err != nil {
					// skip unparseable file
					continue
				}

				pkgDir := gf.dir
				if pkgName == "main" {
					// skip main package
					continue
				}

				pkgsMu.Lock()
				if _, ok := pkgs[pkgDir]; !ok {
					pkgs[pkgDir] = Pkg{
						Name:       pkgName,
						ImportPath: filepath.ToSlash(pkgDir[len(gf.srcDir)+len("/"):]),
						Dir:        pkgDir,
					}
				}
				pkgsMu.Unlock()
			}
			wg.Done()
		}()
	}

	for _, srcDir := range build.Default.SrcDirs() {
		err := godirwalk.Walk(srcDir, &godirwalk.Options{
			FollowSymbolicLinks: true,
			Callback: func(osPathname string, de *godirwalk.Dirent) error {
				name := de.Name()
				pathDir := filepath.Dir(osPathname)

				// Symlink not supported by go
				if de.IsSymlink() {
					return filepath.SkipDir
				}

				// Ignore files begin with "_", "." "_test.go" and directory named "testdata"
				// see: https://golang.org/cmd/go/#hdr-Description_of_package_lists

				if de.IsDir() {
					if name[0] == '.' || name[0] == '_' || name == "testdata" || name == "node_modules" {
						return filepath.SkipDir
					}

					if name == "vendor" {
						if workDir != "" {
							if !visibleVendor(workDir, pathDir) {
								return filepath.SkipDir
							}

							return nil
						}

						if opts.NoVendor {
							return filepath.SkipDir
						}
					}

					return nil
				}

				if name[0] == '.' || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					return nil
				}

				if pathDir == srcDir {
					// Cannot put files on $GOPATH/src or $GOROOT/src.
					return nil
				}

				goFileCh <- goFile{
					path:   osPathname,
					dir:    pathDir,
					srcDir: srcDir,
				}
				return nil
			},
		})

		if err != nil {
			return nil, err
		}
	}

	close(goFileCh)
	wg.Wait()
	return pkgs, nil
}
