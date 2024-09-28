package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha512"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	"path/filepath"
)

var totalDeleted int
var totalDeletedSize int64

type DupFile struct {
	Path   string
	Size   int64
	MD5    string
	SHA512 string
}

func dedup(root string) {
	duplicates, err := findDuplicates(root)
	if err != nil {
		log.Fatalf("Error finding duplicates: %v", err)
	}
	log.Info()
	c := 1
	for _, files := range duplicates {
		if len(files) > 1 {
			log.Infof("Duplicate file %d", c)
			for _, file := range files {
				log.Infof("%s, %d, %s", file.Path, file.Size, file.MD5)
			}
			if err := DeleteDuplicateFiles(files); err != nil {
				log.Fatalf("Error deleting duplicate files: %v", err)
			}
			log.Info()
			c++
		}
	}
	log.Infof("Total: %d files, %d GB", totalDeleted, totalDeletedSize>>30)
}

func remove(dupFile *DupFile) {
	if TheConfig.DoRemove {
		if err := os.Remove(dupFile.Path); err != nil {
			log.Fatalf("Error deleting file %s: %v", dupFile.Path, err)
		}
		log.Infof("Deleted: %s", dupFile.Path)
	} else {
		log.Infof("Would delete: %s", dupFile.Path)
	}
	totalDeleted++
	totalDeletedSize += dupFile.Size
}

func DeleteDuplicateFiles(files []*DupFile) error {
	if len(files) <= 1 {
		return nil
	}
	log.Debugf("Computing SHA512 %s", files[0].Path)
	firstHash, err := computeSHA512(files[0].Path)
	if err != nil {
		return err
	}
	log.Tracef("SHA512 %s: %x", files[0].Path, firstHash)
	for _, dupFile := range files[1:] {
		log.Debugf("Computing SHA512 %s", dupFile.Path)
		hash, err := computeSHA512(dupFile.Path)
		if err != nil {
			return err
		}
		log.Tracef("SHA512 %s: %x", dupFile.Path, hash)
		dupFile.SHA512 = fmt.Sprintf("%x", hash)
		if !bytes.Equal(hash, firstHash) {
			log.Fatalf("Files %s and %s have different content", files[0].Path, dupFile.Path)
			return errors.New("files have different content")
		}
	}
	longestFileName := ""
	shortestDir := ""
	for _, dupFile := range files {
		currFileName := filepath.Base(dupFile.Path)
		currPath := filepath.Dir(dupFile.Path)
		if len(currFileName) > len(longestFileName) {
			longestFileName = currFileName
		}
		if shortestDir == "" || len(currPath) < len(shortestDir) {
			shortestDir = currPath
		}
	}
	c := 0
	var sameDir []*DupFile
	for _, dupFile := range files {
		if filepath.Dir(dupFile.Path) == shortestDir {
			sameDir = append(sameDir, dupFile)
			continue
		}
		remove(dupFile)
		c++
		if c == len(files)-1 {
			break
		}
	}
	if len(sameDir) >= 1 {
		for _, file := range sameDir[1:] {
			remove(file)
		}
		oldFileName := filepath.Base(sameDir[0].Path)
		if len(oldFileName) < len(longestFileName) {
			newPath := filepath.Join(shortestDir, longestFileName)
			if TheConfig.DoRemove {
				if err := os.Rename(sameDir[0].Path, newPath); err != nil {
					log.Fatalf("Error renaming file %s to %s: %v", sameDir[0].Path, newPath, err)
				}
				log.Infof("Renamed: %s to %s", sameDir[0].Path, newPath)
			} else {
				log.Infof("Would rename: %s to %s", sameDir[0].Path, newPath)
			}
		}
	}

	return nil
}

func findDuplicates(root string) (map[string][]*DupFile, error) {
	sizeMap := make(map[int64][]string)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		ext := filepath.Ext(path)
		if TheConfig.IsExcluded(ext) {
			return nil
		}

		size := info.Size()
		if size >= TheConfig.MinSize {
			sizeMap[size] = append(sizeMap[size], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Infof("Total files: %d", len(sizeMap))

	hashMap := make(map[string][]*DupFile)

	for size, files := range sizeMap {
		if len(files) < 2 {
			continue
		}
		for _, file := range files {
			log.Debugf("Computing MD5 %s", file)
			hash, err := computeMD5(file)
			if err != nil {
				log.Fatalf("Error computing MD5 for file %s: %v", file, err)
				continue
			}
			log.Tracef("MD5 %s: %s", file, hash)
			hashMap[hash] = append(hashMap[hash], &DupFile{Path: file, Size: size, MD5: hash})
		}
	}

	return hashMap, nil
}

func computeMD5(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Fatalf("Error closing file %s: %v", f.Name(), err)
		}
	}(f)

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func computeSHA512(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Fatalf("Error closing file %s: %v", f.Name(), err)
		}
	}(f)

	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
