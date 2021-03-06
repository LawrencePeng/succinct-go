package util

import (
	"bytes"
	"encoding/binary"
	"math"
)

type DeltaIntVector struct {
	Samples			*IntVector
	DeltaOffsets	*IntVector
	Deltas			*BitVector
	SamplingRate	int32
}

func NewDeltaIntVector(elements []int32 , samplingRate int32) *DeltaIntVector {
	div := &DeltaIntVector{
		SamplingRate:samplingRate,
	}
	div.encode(elements, 0, int32(len(elements)))
	return div
}

func NewDeltaIntVectorFull(data []int32, startOffset ,length, samplingRate int32) *DeltaIntVector {
	div := &DeltaIntVector{
		SamplingRate:samplingRate,
	}
	div.encode(data, startOffset, length)
	return div
}

func (div *DeltaIntVector) encode(elements []int32, startingOffset int32, length int32) {
	if length == 0 {
		return
	}

	samplesBuf := NewIntArrayList()
	deltasBuf := NewIntArrayList()
	deltaOffsetsBuf := NewIntArrayList()

	maxSample := int32(0)
	lastValue := int32(0)
	totalDeltaCount := int32(0)
	deltaCount := int32(0)
	cumulativeDeltaSize := int32(0)
	maxOffset := int32(0)

	for i := int32(0); i < length; i++ {
		if i % div.SamplingRate == 0 {
			samplesBuf.Add(elements[startingOffset + i])
			if elements[startingOffset + i] > maxSample {
				maxSample = elements[startingOffset + i]
			}
			if cumulativeDeltaSize > maxOffset {
				maxOffset = cumulativeDeltaSize
			}
			deltaOffsetsBuf.Add(cumulativeDeltaSize)
			if i != int32(0) {
				totalDeltaCount += deltaCount
				deltaCount = 0
			}
		} else {
			delta := elements[startingOffset + i] - lastValue
			deltasBuf.Add(delta)
			cumulativeDeltaSize += EncodingSize(delta)
			deltaCount++
		}
		lastValue = elements[startingOffset + i]
	}
	totalDeltaCount += deltaCount

	sampleBits := BitWidth(int64(maxSample))
	deltaOffsetBits := BitWidth(int64(maxOffset))

	if samplesBuf.Size() == 0 {
		div.Samples = nil
	} else {
		div.Samples = CopyFromIntArrayList(samplesBuf, sampleBits)
	}

	if cumulativeDeltaSize == 0 {
		div.Deltas = nil
	} else {
		div.Deltas = NewBitVectorWithSize(int64(cumulativeDeltaSize + 16))
		div.encodeDeltas(deltasBuf)
	}

	if deltaOffsetBits == 0 {
		div.DeltaOffsets = nil
	} else {
		div.DeltaOffsets = CopyFromIntArrayList(deltaOffsetsBuf, deltaOffsetBits)
	}

}
func (div *DeltaIntVector) encodeDeltas(deltasArr *IntArrayList) {
	pos := int64(0)
	for i := int32(0); i < deltasArr.Size(); i++ {
		deltaBits := int32(BitWidth(int64(deltasArr.Get(i))) - 1)
		pos += int64(deltaBits)

		div.Deltas.SetBit(pos)
		pos++
		div.Deltas.SetValue(pos, int64(deltasArr.Get(i)) - int64(1) << uint32(deltaBits), deltaBits)
		pos += int64(deltaBits)
	}
}

func (div *DeltaIntVector) prefixSum(deltaOffset, untilIdx int32) int32 {
	deltaSum := int32(0)
	deltaIdx := int64(0)
	currentDeltaOffset := deltaIdx
	for ;deltaIdx != int64(untilIdx); {
		block := int32(div.Deltas.GetValue(currentDeltaOffset, 16))
		cnt := PreCount(block)
		if cnt == 0 {
			deltaWidth := int32(0)
			for ;div.Deltas.GetBit(currentDeltaOffset) != 1; {
				deltaWidth++
				currentDeltaOffset++
			}
			currentDeltaOffset++
			deltaSum += int32(div.Deltas.GetValue(currentDeltaOffset, deltaWidth)) + int32(int64(1) << uint64(deltaWidth))
			currentDeltaOffset += int64(deltaWidth)
			deltaIdx += 1
		} else if deltaIdx + int64(cnt) <= int64(untilIdx) {
			deltaSum += PreSum(block)
			currentDeltaOffset += int64(PreOffset(block))
			deltaIdx += int64(cnt)
		} else {
			for ; deltaIdx != int64(untilIdx); {
				deltaWidth := int32(0)
				for ; div.Deltas.GetBit(currentDeltaOffset) != 1;  {
					deltaWidth ++
					currentDeltaOffset++
				}
				currentDeltaOffset++
				deltaSum += int32(div.Deltas.GetValue(currentDeltaOffset, deltaWidth) + int64(1) << uint64(deltaWidth))
				currentDeltaOffset += int64(deltaWidth)
				deltaIdx += 1
			}
		}
	}

	return deltaSum
}

func (div *DeltaIntVector) Get(i int32) int32 {
	sampleIdx := i / div.SamplingRate
	deltaOffsetIdx := i % div.SamplingRate
	val := div.Samples.Get(sampleIdx)

	if deltaOffsetIdx == 0 {
		return val
	}

	deltaOffset := div.DeltaOffsets.Get(sampleIdx)
	val += div.prefixSum(deltaOffset, deltaOffsetIdx)
	return val
}

func (div *DeltaIntVector) SerializedSize() int32 {
	var samplesSize, deltaOffsetSize, deltaSize int32
	if div.Samples == nil {
		samplesSize = 4
	} else {
		samplesSize = div.Samples.SerializedSize()
	}

	if div.DeltaOffsets == nil {
		deltaOffsetSize = 4
	} else {
		deltaOffsetSize = div.DeltaOffsets.SerializedSize()
	}

	if div.Deltas == nil {
		deltaSize = 4
	} else {
		deltaSize = div.Deltas.SerializedSize()
	}

	return 4 + samplesSize + deltaOffsetSize + deltaSize
}

func (div *DeltaIntVector) WriteToBuf(buf *bytes.Buffer)  {
	WriteInt(buf, div.SamplingRate)
	if div.Samples != nil {
		div.Samples.WriteToBuf(buf)
	} else {
		WriteInt(buf, 0)
	}

	if div.DeltaOffsets != nil {
		div.DeltaOffsets.WriteToBuf(buf)
	} else {
		WriteInt(buf, 0)
	}

	if div.Deltas != nil {
		div.Deltas.WriteToBuf(buf)
	} else {
		WriteInt(buf, 0)
	}
}

func DIVPrefixSum(deltas []int64, deltaOffset int32, untilIdx int32) int32 {
	deltaSum := int32(0)
	deltaIdx := int64(0)

	currentDeltaOffset := int64(deltaOffset)
	for ; deltaIdx != int64(untilIdx); {
		block := int32(BitVecGetValue(deltas, currentDeltaOffset, 16))
		cnt := PreCount(block)
		if cnt == 0 {
			deltaWidth := int32(0)
			for ; BitVecGetBit(deltas, currentDeltaOffset) != 1; {
				deltaWidth++
				currentDeltaOffset++
			}

			currentDeltaOffset++
			deltaSum +=
				int32(BitVecGetValue(deltas, currentDeltaOffset, deltaWidth) + int64(1) << uint(deltaWidth))
			currentDeltaOffset += int64(deltaWidth)
			deltaIdx ++
		} else if deltaIdx + int64(cnt) <= int64(untilIdx) {
			deltaSum += PreSum(block)
			currentDeltaOffset += int64(PreOffset(block))
			deltaIdx += int64(cnt)
		} else {
			for ;deltaIdx != int64(untilIdx);  {
				deltaWidth := int32(0)
				for ; BitVecGetBit(deltas, currentDeltaOffset) != 1; {
					deltaWidth++
					currentDeltaOffset++
				}
				currentDeltaOffset++
				deltaSum +=
					int32(BitVecGetValue(deltas, currentDeltaOffset, deltaWidth) + int64(1) << uint(deltaWidth))
				currentDeltaOffset += int64(deltaWidth)
				deltaIdx += 1
			}
		}
	}
	return deltaSum
}

func ToLongSlice(bts []byte) []int64 {
	length := len(bts)
	size := length / LONG_SIZE
	ret := make([]int64, size)
	for i := 0; i < size; i++ {
		ret[i] = int64(binary.BigEndian.Uint64(bts[LONG_SIZE * i : LONG_SIZE * i + LONG_SIZE]))
	}
	return ret
}

func ToIntSlice(bts []byte) []int32 {
	length := len(bts)
	size := length / INT_SIZE
	ret := make([]int32, size)
	for i := 0; i < size; i++ {
		ret[i] = int32(binary.BigEndian.Uint32(bts[INT_SIZE * i : INT_SIZE * i + INT_SIZE]))
	}
	return ret
}

func ReadIntFromReader(rdr *bytes.Reader) int32 {
	bts := make([]byte, INT_SIZE)
	rdr.Read(bts)
	return BytesToInt(bts)
}

func ReadIntAtFromReader(rdr *bytes.Reader, off int64) int32 {
	bts := make([]byte, INT_SIZE)
	rdr.ReadAt(bts, off)
	return BytesToInt(bts)
}

func NextFromReader(rdr *bytes.Reader, n int) []byte {
	bts := make([]byte, n)
	rdr.Read(bts)
	return bts
}

func NextAtFromReader(rdr *bytes.Reader, off int64, n int) []byte {
	bts := make([]byte, n)
	rdr.ReadAt(bts, off)
	return bts
}

func BytesToInt(bts []byte) int32 {
	return int32(binary.BigEndian.Uint32(bts))
}

func DIVGet(bts *[]byte, i int32) int32 {
	pos := int64(0)

	samplingRate := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	sampleBits := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	sampleBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	samples := ToLongSlice((*bts)[pos:pos + int64(sampleBlocks) * int64(LONG_SIZE)])
	pos += int64(sampleBlocks) * int64(LONG_SIZE)

	samplesIdx := i / samplingRate
	deltaOffsetsIdx := i % samplingRate
	val := IntVecGet(samples, samplesIdx, sampleBits)

	if deltaOffsetsIdx == 0 {
		return val
	}

	deltaOffsetBits := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltaOffsetBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltaOffsets := ToLongSlice((*bts)[pos : pos + int64(deltaOffsetBlocks) * int64(LONG_SIZE)])
	pos += int64(deltaOffsetBlocks) * int64(LONG_SIZE)


	deltaBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltas := ToLongSlice((*bts)[pos: pos + int64(LONG_SIZE) * int64(deltaBlocks)])
	deltaOffset := IntVecGet(deltaOffsets, samplesIdx, deltaOffsetBits)
	val += DIVPrefixSum(deltas, deltaOffset, deltaOffsetsIdx)

	return val
}

func binarySearchSamples(samples []int64, sampleBits, val, s, e int32) int32 {
	sp := s
	ep := e
	var m, sVal int32

	for ; sp <= ep; {
		m = (sp + ep) / 2
		sVal = IntVecGet(samples, m, sampleBits)
		if sVal == val {
			ep = m
			break
		} else if val < sVal {
			ep = m - 1
		} else {
			sp = m + 1
		}
	}
	if ep < 0 {
		ep = 0
	}
	return ep
}

func BinarySearch(bts *[]byte, val, startIdx, endIdx int32, flag bool) int32 {
	if endIdx < startIdx {
		return endIdx
	}
	pos := int(0)
	samplingRate := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	sampleBits := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	sampleBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	samples := ToLongSlice((*bts)[pos : pos + int(sampleBlocks) * LONG_SIZE])
	pos += int(sampleBlocks) * LONG_SIZE

	sampleOffset := binarySearchSamples(samples, sampleBits, val, startIdx / samplingRate, endIdx / samplingRate)
	deltaLimit := int32(math.Min(float64(endIdx - (sampleOffset * samplingRate)), float64(samplingRate)))

	deltaOffsetBits := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltaOffsetBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltaOffsets := ToLongSlice((*bts)[pos : pos + LONG_SIZE * int(deltaOffsetBlocks)])
	pos += LONG_SIZE * int(deltaOffsetBlocks)

	currentDeltaOffset := IntVecGet(deltaOffsets, sampleOffset, deltaOffsetBits)

	val -= IntVecGet(samples, sampleOffset, sampleBits)

	deltaIdx := int32(0)
	deltaSum := int32(0)

	deltaBlocks := BytesToInt((*bts)[pos: pos + 4])
	pos += 4

	deltas := ToLongSlice((*bts)[pos : pos + LONG_SIZE * int(deltaBlocks)])
	pos += LONG_SIZE * int(deltaBlocks)

	for ; deltaSum < val && deltaIdx < deltaLimit; {
		block := int32(BitVecGetValue(deltas, int64(currentDeltaOffset), 16))
		cnt := PreCount(block)
		block_sum := PreSum(block)

		if cnt == 0 {
			deltaWidth := int32(0)
			for ; BitVecGetBit(deltas, int64(currentDeltaOffset)) != 1; {
				deltaWidth++
				currentDeltaOffset++
			}
			currentDeltaOffset++
			decodedValue := int32(BitVecGetValue(deltas, int64(currentDeltaOffset), deltaWidth) + int64(1) << uint(deltaWidth))
			deltaSum += decodedValue
			currentDeltaOffset += deltaWidth
			deltaIdx++

			if deltaIdx == samplingRate {
				deltaIdx--
				deltaSum -= decodedValue
				break
			}
		} else if deltaSum + block_sum < val && deltaIdx + cnt < deltaLimit {
			deltaSum += block_sum
			currentDeltaOffset += PreOffset(block)
			deltaIdx += cnt
		} else {
			lastDecodedValue := int32(0)
			for ; deltaSum < val && deltaIdx < deltaLimit;  {
				deltaWidth := int32(0)
				for ; BitVecGetBit(deltas, int64(currentDeltaOffset)) != 1;  {
					deltaWidth++
					currentDeltaOffset++
				}
				currentDeltaOffset++
				lastDecodedValue =
					int32(BitVecGetValue(deltas, int64(currentDeltaOffset), deltaWidth) + int64(1) << uint(deltaWidth))
				deltaSum += lastDecodedValue
				currentDeltaOffset += deltaWidth
				deltaIdx++
			}

			if deltaIdx == samplingRate {
				deltaIdx--
				deltaSum -= lastDecodedValue
				break
			}
		}
	}

	ret := sampleOffset * samplingRate + deltaIdx

	if val == deltaSum {
		return ret
	}

	if flag {
		if deltaSum < val {
			return ret
		} else {
			return ret - 1
		}
	} else {
		if deltaSum > val {
			return ret
		} else {
			return ret + 1
		}
	}
}