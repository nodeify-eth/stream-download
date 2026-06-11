package source

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3 struct {
	client *s3.Client
	bucket string
	key    string
}

func NewS3(client *s3.Client, bucket, key string) *S3 {
	return &S3{client: client, bucket: bucket, key: key}
}

func NewDefaultS3(ctx context.Context, bucket, key, endpoint string) (*S3, error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return NewS3(client, bucket, key), nil
}

func (s *S3) Resolve(ctx context.Context) (Identity, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key)})
	if err != nil {
		return Identity{}, err
	}
	id := Identity{Kind: "s3", URL: fmt.Sprintf("s3://%s/%s", s.bucket, s.key), Size: aws.ToInt64(out.ContentLength)}
	if out.ETag != nil {
		id.ETag = aws.ToString(out.ETag)
	}
	if out.VersionId != nil {
		id.VersionID = aws.ToString(out.VersionId)
	}
	return id, nil
}

func (s *S3) ReadRange(ctx context.Context, r Range, pinned Identity) (io.ReadCloser, Identity, error) {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", r.Start, r.End)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key), Range: aws.String(rangeHeader)})
	if err != nil {
		return nil, Identity{}, err
	}
	return out.Body, pinned, nil
}
