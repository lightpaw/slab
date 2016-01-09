package slab

import (
	"reflect"
	"sync/atomic"
	"unsafe"
)

// Pool is a lock-free slab allocator.
type Pool struct {
	classes []class
	minSize int
	maxSize int
}

// NewPool create a new memory pool.
// minSize is the smallest chunk size.
// maxSize is the lagest chunk size.
// factor is used to control growth of chunk size.
// pageSize is the memory size of each slab class.
func NewPool(minSize, maxSize, factor, pageSize int) *Pool {
	pool := &Pool{make([]class, 0, 10), minSize, maxSize}
	chunkSize := minSize
	for {
		c := class{
			size:   chunkSize,
			page:   make([]byte, pageSize),
			chunks: make([]chunk, pageSize/chunkSize),
			head:   (1 << 32),
		}
		for i := 0; i < len(c.chunks); i++ {
			chk := &c.chunks[i]
			// lock down the capacity to protect append operation
			chk.mem = c.page[i*chunkSize : (i+1)*chunkSize : (i+1)*chunkSize]
			if i < len(c.chunks)-1 {
				chk.next = uint64(i+1+1 /* index start from 1 */) << 32
			} else {
				c.pageBegin = uintptr(unsafe.Pointer(&c.page[0]))
				c.pageEnd = uintptr(unsafe.Pointer(&chk.mem[0]))
			}
		}
		pool.classes = append(pool.classes, c)

		chunkSize *= factor
		if chunkSize > maxSize {
			break
		}
	}
	return pool
}

// Alloc try alloc a []byte from internal slab class if no free chunk in slab class Alloc will make one.
func (pool *Pool) Alloc(size int) []byte {
	if size <= pool.maxSize {
		capacity := size
		if capacity < pool.minSize {
			capacity = pool.minSize
		}
		for i := 0; i < len(pool.classes); i++ {
			if pool.classes[i].size >= capacity {
				mem := pool.classes[i].Pop()
				if mem != nil {
					return mem[:size]
				}
				break
			}
		}
	}
	return make([]byte, size)
}

// Free release a []byte that alloc from Pool.Alloc.
func (pool *Pool) Free(mem []byte) {
	capacity := cap(mem)
	for i := 0; i < len(pool.classes); i++ {
		if pool.classes[i].size == capacity {
			pool.classes[i].Push(mem)
			break
		}
	}
}

type class struct {
	size      int
	page      []byte
	pageBegin uintptr
	pageEnd   uintptr
	chunks    []chunk
	head      uint64
}

type chunk struct {
	mem  []byte
	aba  uint32 // reslove ABA problem
	next uint64
}

func (c *class) Push(mem []byte) {
	ptr := (*reflect.SliceHeader)(unsafe.Pointer(&mem)).Data
	if c.pageBegin <= ptr && ptr <= c.pageEnd {
		i := (ptr - c.pageBegin) / uintptr(c.size)
		chk := &c.chunks[i]
		//fmt.Fprintf(os.Stderr, "push: %x\n", ptr)
		if uintptr(unsafe.Pointer(&chk.mem[0])) != ptr {
			panic("slab.Pool: Bad Chunk")
		}
		if chk.next != 0 {
			panic("slab.Pool: Double Free")
		}
		chk.aba++
		new := uint64(i+1)<<32 + uint64(chk.aba)
		for {
			old := atomic.LoadUint64(&c.head)
			chk.next = old
			if atomic.CompareAndSwapUint64(&c.head, old, new) {
				break
			}
		}
	}
}

func (c *class) Pop() []byte {
	for {
		old := atomic.LoadUint64(&c.head)
		if old == 0 {
			return nil
		}
		chk := &c.chunks[old>>32-1]
		if atomic.CompareAndSwapUint64(&c.head, old, chk.next) {
			chk.next = 0
			return chk.mem
		}
	}
}
