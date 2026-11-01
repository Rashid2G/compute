package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// WorkerResult stores metrics from running benchmark tests directly on a system (Host or VM)
type WorkerResult struct {
	Target               string  `json:"target"` // "host" or "vm"
	CPUSHA256MBps        float64 `json:"cpu_sha256_mbps"`
	CPUPrimeDurationMs   float64 `json:"cpu_prime_duration_ms"`
	CPUPrimeOpsPerSec    float64 `json:"cpu_prime_ops_per_sec"`
	CPUMatrixDurationMs  float64 `json:"cpu_matrix_duration_ms"`
	CPUMatrixMFLOPS      float64 `json:"cpu_matrix_mflops"`
	MemSeqBandwidthMBps  float64 `json:"mem_seq_bandwidth_mbps"`
	MemCopyBandwidthMBps float64 `json:"mem_copy_bandwidth_mbps"`
	DiskSeqWriteMBps     float64 `json:"disk_seq_write_mbps"`
	DiskSeqReadMBps      float64 `json:"disk_seq_read_mbps"`
	DiskRandom4KIOPS     float64 `json:"disk_random_4k_iops"`
	DiskRandom4KAvgMs    float64 `json:"disk_random_4k_avg_ms"`
}

// RunWorkerBenchmarks executes all CPU, Memory, and Disk benchmarks and returns a WorkerResult
func RunWorkerBenchmarks(target string, scratchDir string) (WorkerResult, error) {
	if scratchDir == "" {
		scratchDir = filepath.Join(os.TempDir(), "compute-bench-scratch")
	}
	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		return WorkerResult{}, fmt.Errorf("failed to create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratchDir)

	res := WorkerResult{Target: target}

	// 1. CPU: SHA-256 Hashing throughput (64 MB)
	shaMBps, err := benchCPUSHA256(64)
	if err == nil {
		res.CPUSHA256MBps = shaMBps
	}

	// 2. CPU: Prime Sieve (up to 150,000)
	primeMs, primeOps := benchCPUPrimes(150000, 5)
	res.CPUPrimeDurationMs = primeMs
	res.CPUPrimeOpsPerSec = primeOps

	// 3. CPU: Floating-point Matrix Multiplication (200x200)
	matrixMs, mflops := benchCPUMatrix(200)
	res.CPUMatrixDurationMs = matrixMs
	res.CPUMatrixMFLOPS = mflops

	// 4. Memory: Sequential write and read bandwidth (64 MB)
	memSeqMBps := benchMemSeq(64, 4)
	res.MemSeqBandwidthMBps = memSeqMBps

	// 5. Memory: Memory copy (copy src to dst, 512 MB total)
	memCopyMBps := benchMemCopy(32, 16)
	res.MemCopyBandwidthMBps = memCopyMBps

	// 6. Disk: Sequential write with sync (32 MB)
	seqWriteMBps, err := benchDiskSeqWrite(scratchDir, 32)
	if err == nil {
		res.DiskSeqWriteMBps = seqWriteMBps
	}

	// 7. Disk: Sequential read (32 MB)
	seqReadMBps, err := benchDiskSeqRead(scratchDir, 32)
	if err == nil {
		res.DiskSeqReadMBps = seqReadMBps
	}

	// 8. Disk: 4K Random write IOPS with sync (500 writes)
	randIOPS, avgLatencyMs, err := benchDiskRandom4K(scratchDir, 16, 500)
	if err == nil {
		res.DiskRandom4KIOPS = randIOPS
		res.DiskRandom4KAvgMs = avgLatencyMs
	}

	return res, nil
}

// benchCPUSHA256 hashes dataMB megabytes in 64KB blocks and returns MB/s
func benchCPUSHA256(dataMB int) (float64, error) {
	blockSize := 64 * 1024
	block := make([]byte, blockSize)
	for i := range block {
		block[i] = byte(i % 256)
	}

	totalBytes := int64(dataMB) * 1024 * 1024
	blocks := int(totalBytes / int64(blockSize))

	h := sha256.New()
	t0 := time.Now()
	for i := 0; i < blocks; i++ {
		h.Write(block)
	}
	_ = h.Sum(nil)
	dur := time.Since(t0)

	if dur <= 0 {
		return 0, nil
	}
	mbps := float64(dataMB) / dur.Seconds()
	return mbps, nil
}

// benchCPUPrimes runs Sieve of Eratosthenes up to maxPrime iterations times
func benchCPUPrimes(maxPrime int, iters int) (float64, float64) {
	durations := make([]time.Duration, iters)
	for iter := 0; iter < iters; iter++ {
		t0 := time.Now()
		sieve := make([]bool, maxPrime+1)
		for i := 2; i*i <= maxPrime; i++ {
			if !sieve[i] {
				for j := i * i; j <= maxPrime; j += i {
					sieve[j] = true
				}
			}
		}
		durations[iter] = time.Since(t0)
	}

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	avgDur := sum / time.Duration(iters)
	avgMs := float64(avgDur.Nanoseconds()) / 1e6
	ops := 0.0
	if avgDur > 0 {
		ops = 1.0 / avgDur.Seconds()
	}
	return avgMs, ops
}

// benchCPUMatrix performs NxN matrix multiplication and returns duration ms and MFLOPS
func benchCPUMatrix(n int) (float64, float64) {
	a := make([][]float64, n)
	b := make([][]float64, n)
	c := make([][]float64, n)
	for i := 0; i < n; i++ {
		a[i] = make([]float64, n)
		b[i] = make([]float64, n)
		c[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			a[i][j] = float64(i + j)
			b[i][j] = float64(i * j)
		}
	}

	t0 := time.Now()
	for i := 0; i < n; i++ {
		for k := 0; k < n; k++ {
			aik := a[i][k]
			for j := 0; j < n; j++ {
				c[i][j] += aik * b[k][j]
			}
		}
	}
	dur := time.Since(t0)

	durMs := float64(dur.Nanoseconds()) / 1e6
	// 2 * N^3 operations (multiply and add)
	totalOps := 2.0 * float64(n) * float64(n) * float64(n)
	mflops := 0.0
	if dur.Seconds() > 0 {
		mflops = (totalOps / dur.Seconds()) / 1e6
	}
	return durMs, mflops
}

// benchMemSeq sequentially writes and reads a buffer of sizeMB
func benchMemSeq(sizeMB int, iters int) float64 {
	bufSize := sizeMB * 1024 * 1024
	buf := make([]byte, bufSize)

	t0 := time.Now()
	for it := 0; it < iters; it++ {
		// Sequential write
		for i := 0; i < bufSize; i += 4096 {
			buf[i] = byte(i + it)
		}
		// Sequential read verification
		var sum byte
		for i := 0; i < bufSize; i += 4096 {
			sum += buf[i]
		}
		_ = sum
	}
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0
	}
	// Total bytes accessed = 2 * bufSize * iters
	totalMB := float64(sizeMB * 2 * iters)
	return totalMB / dur.Seconds()
}

// benchMemCopy copies a block of sizeMB multiple times
func benchMemCopy(blockSizeMB int, iters int) float64 {
	bufSize := blockSizeMB * 1024 * 1024
	src := make([]byte, bufSize)
	dst := make([]byte, bufSize)
	for i := range src {
		src[i] = byte(i % 256)
	}

	t0 := time.Now()
	for it := 0; it < iters; it++ {
		copy(dst, src)
	}
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0
	}
	totalMB := float64(blockSizeMB * iters)
	return totalMB / dur.Seconds()
}

// benchDiskSeqWrite writes a file of sizeMB with fdatasync/Sync and returns MB/s
func benchDiskSeqWrite(dir string, sizeMB int) (float64, error) {
	filePath := filepath.Join(dir, "bench-seq-write.dat")
	defer os.Remove(filePath)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	blockSize := 64 * 1024
	buf := make([]byte, blockSize)
	for i := range buf {
		buf[i] = 0xAA
	}

	blocks := (sizeMB * 1024 * 1024) / blockSize

	t0 := time.Now()
	for i := 0; i < blocks; i++ {
		if _, err := f.Write(buf); err != nil {
			return 0, err
		}
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0, nil
	}
	return float64(sizeMB) / dur.Seconds(), nil
}

// benchDiskSeqRead reads a previously written file of sizeMB and returns MB/s
func benchDiskSeqRead(dir string, sizeMB int) (float64, error) {
	filePath := filepath.Join(dir, "bench-seq-read.dat")
	defer os.Remove(filePath)

	blockSize := 64 * 1024
	buf := make([]byte, blockSize)

	// Prepare file
	fPrep, err := os.Create(filePath)
	if err != nil {
		return 0, err
	}
	blocks := (sizeMB * 1024 * 1024) / blockSize
	for i := 0; i < blocks; i++ {
		_, _ = fPrep.Write(buf)
	}
	_ = fPrep.Sync()
	_ = fPrep.Close()

	// Read test
	f, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	t0 := time.Now()
	readBuf := make([]byte, blockSize)
	for {
		_, err := f.Read(readBuf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0, nil
	}
	return float64(sizeMB) / dur.Seconds(), nil
}

// benchDiskRandom4K performs random 4KB writes with Sync on a preallocated file
func benchDiskRandom4K(dir string, fileSizeMB int, numWrites int) (float64, float64, error) {
	filePath := filepath.Join(dir, "bench-rand-4k.dat")
	defer os.Remove(filePath)

	fileSizeBytes := int64(fileSizeMB * 1024 * 1024)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	if err := f.Truncate(fileSizeBytes); err != nil {
		return 0, 0, err
	}

	block4K := make([]byte, 4096)
	for i := range block4K {
		block4K[i] = 0x55
	}

	maxBlocks := fileSizeBytes / 4096
	t0 := time.Now()
	for i := 0; i < numWrites; i++ {
		offset := (int64(i*97) % maxBlocks) * 4096
		if _, err := f.WriteAt(block4K, offset); err != nil {
			return 0, 0, err
		}
		if i%10 == 0 {
			_ = f.Sync()
		}
	}
	_ = f.Sync()
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0, 0, nil
	}
	iops := float64(numWrites) / dur.Seconds()
	avgLatencyMs := (float64(dur.Nanoseconds()) / 1e6) / float64(numWrites)
	return iops, avgLatencyMs, nil
}

// RunNetworkServer listens on the given port and reads incoming stream, printing MB/s
func RunNetworkServer(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return err
	}
	defer ln.Close()

	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()

	buf := make([]byte, 64*1024)
	var totalBytes int64
	t0 := time.Now()
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
		}
		if err != nil {
			break
		}
	}
	dur := time.Since(t0)
	mbps := 0.0
	if dur.Seconds() > 0 {
		mbps = (float64(totalBytes) / (1024 * 1024)) / dur.Seconds()
	}

	fmt.Printf("{\"network_throughput_mbps\": %.2f, \"total_bytes\": %d, \"duration_s\": %.4f}\n",
		mbps, totalBytes, dur.Seconds())
	return nil
}

// RunNetworkClient sends dataMB megabytes to targetAddr and returns MB/s
func RunNetworkClient(targetAddr string, dataMB int) (float64, error) {
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	blockSize := 64 * 1024
	buf := make([]byte, blockSize)
	for i := range buf {
		buf[i] = 0xAA
	}

	totalBytes := int64(dataMB * 1024 * 1024)
	blocks := int(totalBytes / int64(blockSize))

	t0 := time.Now()
	for i := 0; i < blocks; i++ {
		if _, err := conn.Write(buf); err != nil {
			return 0, err
		}
	}
	dur := time.Since(t0)

	if dur.Seconds() <= 0 {
		return 0, nil
	}
	mbps := float64(dataMB) / dur.Seconds()
	return mbps, nil
}
