package main

import (
	"github.com/50521136/aegis-dns/server/internal/model"
)

// modelSettings 与 defaultSettings 是 model 包类型的薄别名。
//
// 这样 main 里不用到处写 model.GlobalSettings，同时保持类型完全一致
// （Go 的类型别名是真别名，不是新类型，可以直接传给 store/api）。
type modelSettings = model.GlobalSettings

func defaultSettings() modelSettings { return model.DefaultSettings() }
