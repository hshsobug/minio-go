// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package minio

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/minio/minio-go/v7/pkg/probe"
)

// Accounter keeps tabs of ongoing data transfer information.
type Accounter struct {
	current int64

	total        int64
	StartTime    time.Time
	startValue   int64
	refreshRate  time.Duration
	currentValue int64
	finishOnce   sync.Once
	IsFinished   chan struct{}

	// sobug 速度属性
	Speed float64
	// 记录上一次调用write方法的时间
	lastWriteTime time.Time
	// 记录上一次调用write方法的传输量
	lastWriteValue int64
}

// NewAccounter - Instantiate a new accounter.
func NewAccounter(total int64) *Accounter {
	acct := &Accounter{
		total:        total,
		StartTime:    time.Now(),
		startValue:   0,
		refreshRate:  time.Millisecond * 200,
		IsFinished:   make(chan struct{}),
		currentValue: -1,
	}
	go acct.writer()
	return acct
}

// write calculate the final speed.
func (a *Accounter) write(current int64) float64 {
	now := time.Now()
	// 计算时间差
	timeDiff := now.Sub(a.lastWriteTime)
	// 计算传输量的差值
	valueDiff := current - a.lastWriteValue
	// 更新最后一次调用write方法的时间和传输量
	a.lastWriteTime = now
	a.lastWriteValue = current
	// 如果时间差大于0，则计算速度
	if timeDiff > 0 {
		speed := float64(valueDiff) / (float64(timeDiff) / float64(time.Second))
		a.Speed = speed
		// log.Printf("Accounter properties:\n\tcurrent: %d\n\ttotal: %d\n\tStartTime: %s\n\tstartValue: %d\n\trefreshRate: %s\n\tcurrentValue: %d\n\tSpeed: %.2f\n\tlastWriteTime: %s\n\tlastWriteValue: %d\n",
		// 	a.current, a.total, a.StartTime, a.startValue, a.refreshRate, a.currentValue, a.Speed, a.lastWriteTime, a.lastWriteValue)
		return speed
	}
	return 0.0
}

// NewAccounter - Instantiate a new accounter.
// func NewAccounter(total int64) *Accounter {
// 	acct := &Accounter{
// 		total:        total,
// 		StartTime:    time.Now(),
// 		startValue:   0,
// 		refreshRate:  time.Millisecond * 200,
// 		IsFinished:   make(chan struct{}),
// 		currentValue: -1,
// 	}
// 	go acct.writer()
// 	return acct
// }
// write calculate the final speed.
// func (a *Accounter) write(current int64) float64 {
// 	fromStart := time.Since(a.StartTime)
// 	currentFromStart := current - a.startValue
// 	if currentFromStart > 0 {
// 		speed := float64(currentFromStart) / (float64(fromStart) / float64(time.Second))
// 		a.Speed = speed
// 		return speed
// 	}
// 	return 0.0
// }

// writer update new accounting data for a specified refreshRate.
func (a *Accounter) writer() {
	a.Update()
	for {
		select {
		case <-a.IsFinished:
			return
		case <-time.After(a.refreshRate):
			a.Update()
		}
	}
}

// AccountStat cantainer for current stats captured.
type AccountStat struct {
	Status      string  `json:"status"`
	Total       int64   `json:"total"`
	Transferred int64   `json:"transferred"`
	Speed       float64 `json:"speed"`
}

func (c AccountStat) JSON() string {
	c.Status = "success"
	accountMessageBytes, e := json.MarshalIndent(c, "", " ")
	log.Println(probe.NewError(e), "Unable to marshal into JSON.")

	return string(accountMessageBytes)
}

func (c AccountStat) String() string {
	speedBox := pb.Format(int64(c.Speed)).To(pb.U_BYTES).String()
	if speedBox == "" {
		speedBox = "0 MB"
	} else {
		speedBox = speedBox + "/s"
	}
	message := fmt.Sprintf("Total: %s, Transferred: %s, Speed: %s", pb.Format(c.Total).To(pb.U_BYTES),
		pb.Format(c.Transferred).To(pb.U_BYTES), speedBox)
	return message
}

// Stat provides current stats captured.
func (a *Accounter) Stat() AccountStat {
	var acntStat AccountStat
	a.finishOnce.Do(func() {
		close(a.IsFinished)
		acntStat.Total = a.total
		acntStat.Transferred = atomic.LoadInt64(&a.current)
		acntStat.Speed = a.write(atomic.LoadInt64(&a.current))
	})
	return acntStat
}

// Update update with new values loaded atomically.
func (a *Accounter) Update() {
	c := atomic.LoadInt64(&a.current)
	if c != a.currentValue {
		a.write(c)
		a.currentValue = c
	}
}

// Set sets the current value atomically.
func (a *Accounter) Set(n int64) *Accounter {
	atomic.StoreInt64(&a.current, n)
	return a
}

// Get gets current value atomically
func (a *Accounter) Get() int64 {
	return atomic.LoadInt64(&a.current)
}

func (a *Accounter) SetTotal(n int64) {
	atomic.StoreInt64(&a.total, n)
}

// Add add to current value atomically.
func (a *Accounter) Add(n int64) int64 {
	return atomic.AddInt64(&a.current, n)
}

// Read implements Reader which internally updates current value.
func (a *Accounter) Read(p []byte) (n int, err error) {
	defer func() {
		// Upload retry can read one object twice; Avoid read to be greater than Total
		if n, t := a.Get(), atomic.LoadInt64(&a.total); t > 0 && n > t {
			a.Set(t)
		}
	}()

	n = len(p)
	a.Add(int64(n))
	return
}

func (a *Accounter) GetSpeed() float64 {
	return a.Speed
}
