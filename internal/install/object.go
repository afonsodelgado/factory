package install

import (
	"bytes"
	"encoding/json"
	"errors"
)

type object struct {
	keys   []string
	values map[string]json.RawMessage
}

func newObject() *object {
	return &object{values: map[string]json.RawMessage{}}
}

func parseObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	open, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if open != json.Delim('{') {
		return nil, errors.New("expected a JSON object")
	}
	o := newObject()
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("expected an object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if !o.has(key) {
			o.keys = append(o.keys, key)
		}
		o.values[key] = value
	}
	return o, nil
}

func (o *object) has(key string) bool {
	_, ok := o.values[key]
	return ok
}

func (o *object) child(key string) (*object, error) {
	raw, ok := o.values[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return newObject(), nil
	}
	return parseObject(raw)
}

func (o *object) set(key string, groups []json.RawMessage, add ...json.RawMessage) {
	all := append(append([]json.RawMessage{}, groups...), add...)
	data, _ := json.Marshal(all)
	if !o.has(key) {
		o.keys = append(o.keys, key)
	}
	o.values[key] = data
}

func (o *object) remove(key string) {
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			return
		}
	}
}

func (o *object) marshal() (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(o.values[key])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
