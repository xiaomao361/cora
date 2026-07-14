package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
)

type position struct {
	Offset int64  `json:"offset"`
	FileID string `json:"file_id"`
}

type positionFile struct {
	Positions map[string]position `json:"positions"`
}

type positionStore struct {
	mu   sync.Mutex
	path string
	file positionFile
}

func openPositionStore(path string) (*positionStore, error) {
	positions, err := loadPositions(path)
	if err != nil {
		return nil, err
	}
	return &positionStore{path: path, file: positions}, nil
}

func (store *positionStore) get(path string) (position, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.file.Positions[path]
	return value, exists
}

func (store *positionStore) put(path string, value position) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.file.Positions[path] = value
	return store.file.save(store.path)
}

func loadPositions(path string) (positionFile, error) {
	result := positionFile{Positions: make(map[string]position)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read positions: %w", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("parse positions: %w", err)
	}
	if result.Positions == nil {
		result.Positions = make(map[string]position)
	}
	return result, nil
}

func (positions positionFile) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create positions directory: %w", err)
	}
	data, err := json.MarshalIndent(positions, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cora-positions-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileID(info os.FileInfo) string {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Struct {
		dev, ino := value.FieldByName("Dev"), value.FieldByName("Ino")
		devValue, devOK := integerValue(dev)
		inoValue, inoOK := integerValue(ino)
		if devOK && inoOK {
			return fmt.Sprintf("%d:%d", devValue, inoValue)
		}
	}
	return fmt.Sprintf("%T", info.Sys())
}

func integerValue(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(value.Int()), true
	default:
		return 0, false
	}
}
