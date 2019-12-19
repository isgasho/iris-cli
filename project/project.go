package project

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kataras/iris-cli/utils"
)

type Project struct {
	// Remote.
	Repo   string `json:"repo" yaml:"Repo" toml:"Repo"`                 // e.g. "github.com/iris-contrib/project1"
	Branch string `json:"branch,omitempty" yaml:"Branch" toml:"Branch"` // if empty then set to "master"
	// Local.
	Dest   string `json:"dest,omitempty" yaml:"Dest" toml:"Dest"`       // if empty then $GOPATH+Module or ./+Module
	Module string `json:"module,omitempty" yaml:"Module" toml:"Module"` // if empty then set to the remote module name fetched from go.mod
}

func New(dest, repo string) *Project {
	return &Project{
		Repo:   repo,
		Branch: "master",
		Dest:   dest,
		Module: "",
	}
}

func (p *Project) Install() error {
	b, err := p.download()
	if err != nil {
		return err
	}

	return p.unzip(b)
}

func (p *Project) download() ([]byte, error) {
	zipURL := fmt.Sprintf("https://%s/archive/%s.zip", p.Repo, p.Branch)
	req, err := http.NewRequest(http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Accept-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// println(resp.Header.Get("Content-Length"))
	// println(resp.ContentLength)

	var reader io.Reader = resp.Body

	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	return ioutil.ReadAll(reader)
}

func (p *Project) unzip(body []byte) error {
	compressedRootFolder := filepath.Base(p.Repo) + "-" + p.Branch // e.g. iris-master
	r, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}

	var oldModuleName []byte
	// Find current module name, starting from the end because list is sorted alphabetically
	// and "go.mod" is more likely to be visible at the end.
	modFile := filepath.Join(compressedRootFolder, "go.mod")
	for i := len(r.File) - 1; i > 0; i-- {
		f := r.File[i]
		if filepath.Clean(f.Name) == modFile {
			rc, err := f.Open()
			if err != nil {
				return err
			}

			contents, err := ioutil.ReadAll(rc)
			if err != nil {
				return err
			}

			oldModuleName = []byte(utils.ModulePath(contents))
			if p.Module == "" {
				// if new module name is empty, then default it to the remote one.
				p.Module = string(oldModuleName)
			}

			break
		}
	}

	var (
		newModuleName = []byte(p.Module)
		shouldReplace = !bytes.Equal(oldModuleName, newModuleName)
	)

	// If destination is empty then set it to $GOPATH+newModuleName.
	gopath := os.Getenv("GOPATH")
	dest := p.Dest
	if dest == "" {
		if gopath != "" {
			dest = filepath.Join(gopath, "src", filepath.Dir(p.Module))
		} else {
			dest, _ = os.Getwd()
		}
	} else {
		dest = strings.Replace(dest, "${GOPATH}", gopath, 1)
		d, err := filepath.Abs(dest)
		if err == nil {
			dest = d
		}
	}
	p.Dest = dest

	for _, f := range r.File {
		// Store filename/path for returning and using later on
		fpath := filepath.Join(dest, f.Name)

		// https://snyk.io/research/zip-slip-vulnerability#go
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		var rc io.ReadCloser

		rc, err = f.Open()
		if err != nil {
			return err
		}

		// If new(local) module name differs the current(remote) one.
		if shouldReplace {
			contents, err := ioutil.ReadAll(rc)
			if err != nil {
				return err
			}

			newContents := bytes.ReplaceAll(contents, oldModuleName, newModuleName)
			rc = utils.NoOpReadCloser(bytes.NewReader(newContents))
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}

	newpath := filepath.Join(dest, filepath.Base(p.Module))
	os.RemoveAll(newpath)
	return os.Rename(filepath.Join(dest, compressedRootFolder), newpath)
}
