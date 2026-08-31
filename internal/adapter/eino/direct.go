package einoadapter

import (
	"github.com/cloudwego/eino/schema"
)

const emptyReply = "您好，请再说具体一点，我好帮您。"

type directStream struct {
	stream *schema.StreamReader[*schema.Message]
	model  string
}

func (s *directStream) Recv() (string, error) {
	for {
		message, err := s.stream.Recv()
		if err != nil {
			return "", err
		}
		if message.Content != "" {
			return message.Content, nil
		}
	}
}

func (s *directStream) Close()        { s.stream.Close() }
func (s *directStream) Model() string { return s.model }
