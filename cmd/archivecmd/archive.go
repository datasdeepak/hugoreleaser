// Copyright 2022 The Hugoreleaser Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package archivecmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/bep/hugoreleaser/cmd/buildcmd"
	"github.com/bep/hugoreleaser/cmd/corecmd"
	"github.com/bep/hugoreleaser/internal/archives"
	"github.com/bep/hugoreleaser/internal/common/ioh"
	"github.com/bep/hugoreleaser/internal/common/templ"
	"github.com/bep/hugoreleaser/internal/config"
	"github.com/bep/hugoreleaser/pkg/plugins/archiveplugin"

	"github.com/bep/hugoreleaser/pkg/model"
	"github.com/bep/logg"
	"github.com/peterbourgon/ff/v3/ffcli"
)

const commandName = "archive"

// New returns a usable ffcli.Command for the archive subcommand.
func New(core *corecmd.Core) *ffcli.Command {
	fs := flag.NewFlagSet(corecmd.CommandName+" "+commandName, flag.ExitOnError)

	archivist := NewArchivist(core, nil, fs)

	core.RegisterFlags(fs)

	return &ffcli.Command{
		Name:       "archive",
		ShortUsage: corecmd.CommandName + " archive [flags] <action>",
		ShortHelp:  "TODO(bep)",
		FlagSet:    fs,
		Exec:       archivist.Exec,
	}
}

type ArchiveTemplateContext struct {
	model.BuildContext
}

type Archivist struct {
	infoLog logg.LevelLogger
	core    *corecmd.Core

	buildPaths *buildcmd.BuildPaths

	initOnce sync.Once
	initErr  error
}

// NewArchivist returns a new Archivist.
func NewArchivist(core *corecmd.Core, buildPaths *buildcmd.BuildPaths, fs *flag.FlagSet) *Archivist {

	if buildPaths == nil {
		buildPaths = &buildcmd.BuildPaths{}
		fs.StringVar(&buildPaths.Paths, "build-paths", "/builds/**", "The builds to handle (defaults to all).")
	}

	a := &Archivist{
		core:       core,
		buildPaths: buildPaths,
	}

	return a
}

func (b *Archivist) Init() error {
	b.initOnce.Do(func() {
		b.infoLog = b.core.InfoLog.WithField("cmd", commandName)
		b.initErr = b.buildPaths.Init()

	})
	return b.initErr
}

func (b *Archivist) Exec(ctx context.Context, args []string) error {
	if err := b.Init(); err != nil {
		return err
	}

	r, _ := b.core.Workforce.Start(ctx)

	archiveDistDir := filepath.Join(
		b.core.DistDir,
		b.core.Config.Project,
		b.core.Tag,
		b.core.DistRootArchives,
	)

	err := b.core.Config.ForEachArchiveArch(b.buildPaths.PathsCompiled, func(archive config.Archive, archPath config.BuildArchPath) error {
		archiveSettings := archive.ArchiveSettings
		arch := archPath.Arch

		r.Run(func() (err error) {
			archiveTemplCtx := ArchiveTemplateContext{
				model.BuildContext{
					Project: b.core.Config.Project,
					Tag:     b.core.Tag,
					Goos:    arch.Os.Goos,
					Goarch:  arch.Goarch,
				},
			}

			name := templ.Sprintt(archive.ArchiveSettings.NameTemplate, archiveTemplCtx)
			name = archiveSettings.ReplacementsCompiled.Replace(name)

			outDir := filepath.Join(archiveDistDir, filepath.FromSlash(archPath.Path))
			if err := ioh.RemoveAllMkdirAll(outDir); err != nil {
				return err
			}

			outFilename := filepath.Join(
				outDir,
				name,
			)

			outFilename += archiveSettings.Type.Extension

			b.infoLog.WithField("file", outFilename).Log(logg.String("Archive"))

			binaryFilename := filepath.Join(
				b.core.DistDir,
				b.core.Config.Project,
				b.core.Tag,
				b.core.DistRootBuilds,
				arch.BinaryPath(),
			)

			if _, err := os.Stat(binaryFilename); err != nil {
				// TODO(bep) add more context here.
				return fmt.Errorf("%s: binary file not found: %q", commandName, binaryFilename)
			}

			if err := os.MkdirAll(filepath.Dir(outFilename), 0o755); err != nil {
				return err
			}

			buildRequest := archiveplugin.Request{
				Settings:    archiveSettings,
				OutFilename: outFilename,
			}

			buildRequest.Files = append(buildRequest.Files, archiveplugin.ArchiveFile{
				SourcePathAbs: binaryFilename,
				TargetPath:    path.Join(archiveSettings.BinaryDir, arch.BuildSettings.Binary),
			})

			for _, extraFile := range archiveSettings.ExtraFiles {
				buildRequest.Files = append(buildRequest.Files, archiveplugin.ArchiveFile{
					// TODO(bep) unify slashes.
					SourcePathAbs: filepath.Join(b.core.ProjectDir, extraFile.SourcePath),
					TargetPath:    extraFile.TargetPath,
				})
			}

			err = archives.Build(
				b.core,
				b.infoLog,
				archiveSettings,
				buildRequest,
			)

			if err != nil {
				return err
			}

			return nil
		})

		return nil
	})

	errWait := r.Wait()

	if err != nil {
		return err
	}

	return errWait
}
