package apiapp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func libraryVideoSeeds() ([]templatelibrary.Entry, error) {
	definitions, err := media.VideoTemplateDefinitions()
	if err != nil {
		return nil, err
	}
	entries := []templatelibrary.Entry{}
	for _, t := range definitions {
		raw, err := json.MarshalIndent(t, "", "  ")
		if err != nil {
			return nil, err
		}
		entries = append(entries, templatelibrary.Entry{ID: templatelibrary.VideoID(t.ID), Kind: "video", Source: "builtin-video", Enabled: true, Fields: templatelibrary.Fields{Name: t.Name.ZH, NameEN: t.Name.EN, Description: t.Description.ZH, DescriptionEN: t.Description.EN, Content: string(raw), Tags: t.Industries}})
	}
	return entries, nil
}
func libraryBody(c *gin.Context, input *templatelibrary.Mutation) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		c.JSON(400, fail(400, "模板字段不合法或内容过大"))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		c.JSON(400, fail(400, "请求必须是单个 JSON 对象"))
		return false
	}
	return true
}
func libraryError(c *gin.Context, deps *Deps, err error) {
	switch {
	case errors.Is(err, templatelibrary.ErrInvalid):
		c.JSON(400, fail(400, err.Error()))
	case errors.Is(err, templatelibrary.ErrConflict):
		c.JSON(409, fail(409, err.Error()))
	case errors.Is(err, templatelibrary.ErrNotFound):
		c.JSON(404, fail(404, err.Error()))
	default:
		internalError(c, deps, err)
	}
}
func registerTemplateLibraryRoutes(router *gin.Engine, deps *Deps) {
	repo := templatelibrary.Repository{Store: deps.Store}
	registerTemplatePackageRoutes(router, deps, repo)
	registerTemplatePreviewRoutes(router, deps)
	router.GET("/console/api/template-library", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		seeds, err := libraryVideoSeeds()
		if err != nil {
			internalError(c, deps, err)
			return
		}
		entries, err := repo.List(c.Request.Context(), seeds)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, okData(gin.H{"entries": entries}))
	})
	mutate := func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var input templatelibrary.Mutation
		if !libraryBody(c, &input) {
			return
		}
		seeds, err := libraryVideoSeeds()
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if input.Preview != nil && !uploadedPreviewExists(deps, *input.Preview) {
			libraryError(c, deps, templatelibrary.ErrInvalid)
			return
		}
		remove := c.Request.Method == "DELETE"
		entry, err := repo.Save(c.Request.Context(), seeds, c.Param("id"), input, remove)
		if err != nil {
			libraryError(c, deps, err)
			return
		}
		action := "template-library.save"
		if remove {
			action = "template-library.delete"
		}
		writeAudit(c.Request.Context(), deps, actor, action, "template", entry.ID, gin.H{"kind": entry.Kind, "enabled": entry.Enabled})
		// 目录已持久化后再通知，客户端补读可看到新版本；冲突与失败不发送。
		deps.ConfigEvents.Emit(events.DomainTemplateLibrary)
		c.JSON(200, okData(entry))
	}
	router.POST("/console/api/template-library", mutate)
	router.PUT("/console/api/template-library/:id", mutate)
	router.DELETE("/console/api/template-library/:id", mutate)
	router.GET("/api/v1/client/scenes", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "client-scenes") {
			return
		}
		scenes, err := repo.Scenes(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, okData(scenes))
	})
	router.GET("/api/v1/client/skills", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "client-skills") {
			return
		}
		skills, err := repo.Skills(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, okData(skills))
	})
}

func registerTemplatePackageRoutes(router *gin.Engine, deps *Deps, repo templatelibrary.Repository) {
	router.POST("/console/api/template-library/packages", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, templatelibrary.MaxPackageSize))
		if err != nil {
			c.JSON(400, fail(400, "ZIP 文件须不超过 4 MiB"))
			return
		}
		info, content, err := repo.ImportPackage(c.Request.Context(), raw)
		if err != nil {
			libraryError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{"package": info, "content": content}))
	})
	download := func(public bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			if public {
				if !allowPublicRead(c, deps, "client-skill-package") {
					return
				}
			} else if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
				return
			}
			entries, err := repo.List(c.Request.Context(), nil)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			for _, entry := range entries {
				if entry.ID != c.Param("id") || entry.Kind != "skill" || entry.Package == nil || (public && !entry.Enabled) {
					continue
				}
				if hash := c.Query("sha256"); hash != "" && hash != entry.Package.SHA256 {
					libraryError(c, deps, templatelibrary.ErrConflict)
					return
				}
				raw, err := repo.Package(c.Request.Context(), entry.Package.SHA256)
				if err != nil {
					libraryError(c, deps, err)
					return
				}
				c.Header("Cache-Control", "no-store")
				c.Header("Content-Disposition", `attachment; filename="skill-`+entry.Package.SHA256[:12]+`.zip"`)
				c.Data(200, "application/zip", raw)
				return
			}
			libraryError(c, deps, templatelibrary.ErrNotFound)
		}
	}
	router.GET("/console/api/template-library/:id/package", download(false))
	router.GET("/api/v1/client/skills/:id/package", download(true))
	router.GET("/api/v1/client/templates", func(c *gin.Context) {
		if !allowPublicRead(c, deps, "client-templates") {
			return
		}
		entries, err := repo.List(c.Request.Context(), nil)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		out := []gin.H{}
		for _, entry := range entries {
			if entry.IsTextTemplate() && entry.Enabled {
				out = append(out, gin.H{"id": entry.ID, "name": entry.Name, "description": entry.Description, "content": entry.Content})
			}
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, okData(out))
	})
}
