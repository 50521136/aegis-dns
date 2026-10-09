package dns

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// cidrIndex 是按来源 IP 反查租户的索引（文档 3.2 的 UDP/TCP 识别路径）。
//
// 实现方式：把 CIDR 按前缀长度分桶，查询时从最长前缀（最具体）向最短遍历，
// 命中即返回。相比线性扫描全部 CIDR，桶数上限是 33（IPv4）或 129（IPv6），
// 与租户数量无关。
type cidrIndex struct {
	mu sync.RWMutex
	v4 map[int]map[uint32]entry
	v6 map[int]map[[16]byte]entry
	// v4Order / v6Order 是已出现的掩码长度，降序排列，避免每次查询都排序。
	v4Order []int
	v6Order []int
}

type entry struct {
	userID   string
	clientID string
}

func newCIDRIndex() *cidrIndex {
	return &cidrIndex{
		v4: map[int]map[uint32]entry{},
		v6: map[int]map[[16]byte]entry{},
	}
}

// Add 登记一条 CIDR 归属。
func (c *cidrIndex) Add(cidr, userID, clientID string) error {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return fmt.Errorf("CIDR %q 非法: %w", cidr, err)
	}
	ones, bits := ipnet.Mask.Size()
	e := entry{userID: userID, clientID: clientID}

	c.mu.Lock()
	defer c.mu.Unlock()

	if bits == 32 {
		key, ok := ip4ToUint32(ipnet.IP.To4())
		if !ok {
			return fmt.Errorf("CIDR %q 的 IPv4 部分非法", cidr)
		}
		if c.v4[ones] == nil {
			c.v4[ones] = map[uint32]entry{}
			c.v4Order = append(c.v4Order, ones)
			sort.Sort(sort.Reverse(sort.IntSlice(c.v4Order)))
		}
		c.v4[ones][key] = e
		return nil
	}
	if bits == 128 {
		var key [16]byte
		copy(key[:], ipnet.IP.To16())
		if c.v6[ones] == nil {
			c.v6[ones] = map[[16]byte]entry{}
			c.v6Order = append(c.v6Order, ones)
			sort.Sort(sort.Reverse(sort.IntSlice(c.v6Order)))
		}
		c.v6[ones][key] = e
		return nil
	}
	return fmt.Errorf("CIDR %q 的掩码位数 %d 非法", cidr, bits)
}

// Lookup 按客户端 IP 反查租户。
//
// 从最长前缀向最短遍历，命中即返回 —— 这样 /32 的精确绑定优先于 /24 的网段绑定。
func (c *cidrIndex) Lookup(ip net.IP) (userID, clientID string, ok bool) {
	if c == nil || ip == nil {
		return "", "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if v4 := ip.To4(); v4 != nil {
		key, _ := ip4ToUint32(v4)
		for _, ones := range c.v4Order {
			masked := key & mask32(ones)
			if e, hit := c.v4[ones][masked]; hit {
				return e.userID, e.clientID, true
			}
		}
		return "", "", false
	}

	if v16 := ip.To16(); v16 != nil {
		var key [16]byte
		copy(key[:], v16)
		for _, ones := range c.v6Order {
			masked := mask16(key, ones)
			if e, hit := c.v6[ones][masked]; hit {
				return e.userID, e.clientID, true
			}
		}
	}
	return "", "", false
}

// Count 返回已登记的 CIDR 条数（用于系统状态展示）。
func (c *cidrIndex) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, m := range c.v4 {
		n += len(m)
	}
	for _, m := range c.v6 {
		n += len(m)
	}
	return n
}

func ip4ToUint32(ip net.IP) (uint32, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}

func mask32(ones int) uint32 {
	if ones <= 0 {
		return 0
	}
	if ones >= 32 {
		return 0xffffffff
	}
	return ^uint32(0) << (32 - ones)
}

func mask16(in [16]byte, ones int) [16]byte {
	var out [16]byte
	full := ones / 8
	rem := ones % 8
	for i := 0; i < full && i < 16; i++ {
		out[i] = in[i]
	}
	if full < 16 && rem > 0 {
		out[full] = in[full] & byte(0xff<<(8-rem))
	}
	return out
}
