/*
 * Copyright 2023 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package idl

import (
	"encoding/binary"
	"net"
	"sync"

	"github.com/cloudwego/shmipc-go"
)

// the idl is the Request struct and Response struct.
// the demo will show hot to use Stream's BufferWriter() and BufferReader() to do serialization and deserialization.

type Request struct {
	ID   uint64
	Name string
	Key  []byte
}

type Response struct {
	ID    uint64
	Name  string
	Image []byte
}

func (r *Request) ReadFromShm(reader shmipc.BufferReader) error {
	//1.read ID
	data, err := reader.ReadBytes(8)
	if err != nil {
		return err
	}
	r.ID = binary.BigEndian.Uint64(data)

	//2.read Name
	data, err = reader.ReadBytes(4)
	if err != nil {
		return err
	}
	strLen := binary.BigEndian.Uint32(data)
	data, err = reader.ReadBytes(int(strLen))
	if err != nil {
		return err
	}
	r.Name = string(data)

	//3. Read Key
	data, err = reader.ReadBytes(4)
	if err != nil {
		return err
	}
	keyLen := binary.BigEndian.Uint32(data)
	data, err = reader.ReadBytes(int(keyLen))
	if err != nil {
		return err
	}
	r.Key = r.Key[:0]
	r.Key = append(r.Key, data...)

	//4. release share memory buffer
	reader.ReleasePreviousRead()
	return nil
}

func (r *Request) WriteToShm(writer shmipc.BufferWriter) error {
	//1.write ID
	data, err := writer.Reserve(8)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint64(data, r.ID)

	//2.write Name
	data, err = writer.Reserve(4)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(data, uint32(len(r.Name)))
	if err = writer.WriteString(r.Name); err != nil {
		return nil
	}

	//3.write Key
	data, err = writer.Reserve(4)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(data, uint32(len(r.Key)))
	if _, err = writer.WriteBytes(r.Key); err != nil {
		return err
	}
	return nil
}

var BufferPool = sync.Pool{New: func() interface{} {
	return make([]byte, 4096)
}}

func (r *Request) Serialize() []byte {
	data := BufferPool.Get().([]byte)
	offset := 0
	binary.BigEndian.PutUint64(data, r.ID)
	offset += 8
	binary.BigEndian.PutUint32(data[offset:], uint32(len(r.Name)))
	offset += 4
	copy(data[offset:], r.Name)
	offset += len(r.Name)
	binary.BigEndian.PutUint32(data[offset:], uint32(len(r.Key)))
	offset += 4
	copy(data[offset:], r.Key)
	offset += len(r.Key)
	return data[:offset]
}

func (r *Request) Deserialize(data []byte) {
	//1.read ID
	offset := 0
	r.ID = binary.BigEndian.Uint64(data[offset:])
	offset += 8

	strLen := binary.BigEndian.Uint32(data[offset:])
	offset += 4
	r.Name = string(data[offset : offset+int(strLen)])
	offset += int(strLen)

	//3. Read Key
	keyLen := binary.BigEndian.Uint32(data[offset:])
	offset += 4
	r.Key = r.Key[:0]
	r.Key = append(r.Key, data[offset:offset+int(keyLen)]...)
}

func (r *Request) Reset() {
	r.ID = 0
	r.Name = ""
	r.Key = r.Key[:0]
}

func (r *Response) ReadFromShm(reader shmipc.BufferReader) error {
	//1.read ID
	data, err := reader.ReadBytes(8)
	if err != nil {
		return err
	}
	r.ID = binary.BigEndian.Uint64(data)

	//2.read Name
	data, err = reader.ReadBytes(4)
	if err != nil {
		return err
	}
	strLen := binary.BigEndian.Uint32(data)
	data, err = reader.ReadBytes(int(strLen))
	if err != nil {
		return err
	}
	r.Name = string(data)

	//3. Read Image
	data, err = reader.ReadBytes(4)
	if err != nil {
		return err
	}
	imageLen := binary.BigEndian.Uint32(data)
	data, err = reader.ReadBytes(int(imageLen))
	if err != nil {
		return err
	}
	r.Image = r.Image[:0]
	r.Image = append(r.Image, data...)

	//4. release share memory buffer
	reader.ReleasePreviousRead()
	return nil
}

func (r *Response) Deserialize(data []byte) {
	//1.read ID
	offset := 0
	r.ID = binary.BigEndian.Uint64(data)
	offset += 8

	//2.read Name
	strLen := binary.BigEndian.Uint32(data[offset:])
	offset += 4
	r.Name = string(data[offset : offset+int(strLen)])
	offset += int(strLen)

	//3. Read Image
	imageLen := binary.BigEndian.Uint32(data[offset:])
	offset += 4
	r.Image = r.Image[:0]
	r.Image = append(r.Image, data[offset:offset+int(imageLen)]...)

}

func (r *Response) Serialize() []byte {
	//1.write ID
	data := BufferPool.Get().([]byte)
	offset := 0
	binary.BigEndian.PutUint64(data, r.ID)
	offset += 8

	//2.write Name
	binary.BigEndian.PutUint32(data[offset:], uint32(len(r.Name)))
	offset += 4
	offset += copy(data[offset:], r.Name)

	//3.write Image
	binary.BigEndian.PutUint32(data[offset:], uint32(len(r.Image)))
	offset += 4
	copy(data[offset:], r.Image)
	offset += len(r.Image)
	return data[:offset]
}

func (r *Response) WriteToShm(writer shmipc.BufferWriter) error {
	//1.write ID
	data, err := writer.Reserve(8)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint64(data, r.ID)

	//2.write Name
	data, err = writer.Reserve(4)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(data, uint32(len(r.Name)))
	if err = writer.WriteString(r.Name); err != nil {
		return nil
	}

	//3.write Image
	data, err = writer.Reserve(4)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(data, uint32(len(r.Image)))
	if _, err = writer.WriteBytes(r.Image); err != nil {
		return err
	}
	return nil
}

func (r *Response) Reset() {
	r.ID = 0
	r.Name = ""
	r.Image = r.Image[:0]
}

func MustWrite(conn net.Conn, data []byte) {
	written := 0
	for written < len(data) {
		n, err := conn.Write(data[written:])
		if err != nil {
			panic(err)
		}
		written += n
	}
}
