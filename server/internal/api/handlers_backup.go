package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"

	"github.com/50521136/aegis-dns/server/internal/version"
	"os"
	"path/filepath"
	"time"
)

// handleAdminBackup 打包下载一份完整备份。
//
// 内容：数据库（VACUUM INTO 出的干净副本）+ config.yaml + 证书目录。
//
// 为什么用 VACUUM INTO 而不是直接读 aegis.db 文件：
// 库是 WAL 模式，直接拷主文件会漏掉还没 checkpoint 的 WAL 内容 ——
// 拷出来的是一份「旧的」数据库，而进程还在跑、没有任何报错。
// VACUUM INTO 会拿一致性快照，且顺带压实碎片。
func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	tmpDir, err := os.MkdirTemp("", "aegis-backup-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "创建临时目录失败: "+err.Error(), nil)
		return
	}
	defer os.RemoveAll(tmpDir)

	dbCopy := filepath.Join(tmpDir, "aegis.db")
	if err := s.store.BackupTo(r.Context(), dbCopy); err != nil {
		writeStoreError(w, err, "导出数据库失败")
		return
	}

	// 先写临时文件再流式下发：整包可能有几十 MB，全塞内存没必要。
	out, err := os.CreateTemp("", "aegis-backup-*.tar.gz")
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "创建备份包失败", nil)
		return
	}
	outPath := out.Name()
	defer os.Remove(outPath)
	defer out.Close()

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	// 1. 数据库
	if err := addFileToTar(tw, dbCopy, "aegis.db"); err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "打包数据库失败: "+err.Error(), nil)
		return
	}
	// 2. 配置文件（可能不存在，忽略）
	if err := addFileToTar(tw, s.cfg.Path, "config.yaml"); err != nil && !os.IsNotExist(err) {
		s.log.Warn("备份时读取配置文件失败", "err", err)
	}
	// 3. 证书目录
	certDir := filepath.Join(s.cfg.DataDir, "certs")
	if entries, derr := os.ReadDir(certDir); derr == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			_ = addFileToTar(tw, filepath.Join(certDir, e.Name()), "certs/"+e.Name())
		}
	}
	// 4. 一份说明，避免拿到包的人不知道这是什么、里面有没有敏感内容
	meta := fmt.Sprintf(
		"# AegisDNS 备份\n生成时间: %s\n版本: %s\n\n内容:\n"+
			"  aegis.db      SQLite 数据库（VACUUM INTO 一致性快照）\n"+
			"  config.yaml   运行配置\n"+
			"  certs/        TLS 证书与私钥\n\n"+
			"注意: 包含用户数据与证书私钥，请按凭据管理。\n"+
			"恢复: 停服务 → 用 aegis.db 覆盖 data/aegis.db → 启服务。\n",
		time.Now().Format(time.RFC3339), version.Get().Version,
	)
	if err := addBytesToTar(tw, []byte(meta), "README.txt"); err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "写入说明失败", nil)
		return
	}

	if err := tw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "收尾失败", nil)
		return
	}
	if err := gz.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "收尾失败", nil)
		return
	}

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "读取备份包失败", nil)
		return
	}
	stat, err := out.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeValidationFailed, "读取备份包失败", nil)
		return
	}

	name := fmt.Sprintf("aegis-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	if _, err := io.Copy(w, out); err != nil {
		s.log.Warn("下发备份包时中断", "err", err)
	}
}

func addFileToTar(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    stat.Size(),
		ModTime: stat.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addBytesToTar(tw *tar.Writer, data []byte, name string) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
