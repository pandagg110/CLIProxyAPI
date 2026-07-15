package loguploader

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/codes"
)

var ErrObjectConflict = errors.New("TOS object already exists and overwrite is forbidden")

const archiveChecksumMetadataKey = "cliproxy-sha256"

type tosObjectClient interface {
	PutObjectFromFile(context.Context, *tos.PutObjectFromFileInput) (*tos.PutObjectFromFileOutput, error)
	HeadObjectV2(context.Context, *tos.HeadObjectV2Input) (*tos.HeadObjectV2Output, error)
}

// TOSUploader uploads archives through the native Volcengine TOS endpoint.
type TOSUploader struct {
	client tosObjectClient
}

func NewTOSUploader(cfg UploadConfig) (*TOSUploader, error) {
	endpoint, errEndpoint := parseTOSEndpoint(cfg.Endpoint)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	credentials, errCredentials := loadTOSCredentials(cfg)
	if errCredentials != nil {
		return nil, errCredentials
	}
	client, errClient := tos.NewClientV2(endpoint,
		tos.WithRegion(cfg.Region),
		tos.WithCredentials(credentials),
	)
	if errClient != nil {
		return nil, fmt.Errorf("create TOS client: %w", errClient)
	}
	return &TOSUploader{client: client}, nil
}

func loadTOSCredentials(cfg UploadConfig) (*tos.StaticCredentials, error) {
	accessKeyID := strings.TrimSpace(os.Getenv(cfg.AccessKeyIDEnv))
	if accessKeyID == "" {
		return nil, fmt.Errorf("environment variable %s is required", cfg.AccessKeyIDEnv)
	}
	secretAccessKey := strings.TrimSpace(os.Getenv(cfg.SecretAccessKeyEnv))
	if secretAccessKey == "" {
		return nil, fmt.Errorf("environment variable %s is required", cfg.SecretAccessKeyEnv)
	}
	credentials := tos.NewStaticCredentials(accessKeyID, secretAccessKey)
	if sessionToken := strings.TrimSpace(os.Getenv(cfg.SessionTokenEnv)); sessionToken != "" {
		credentials.WithSecurityToken(sessionToken)
	}
	return credentials, nil
}

func parseTOSEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil {
		return "", fmt.Errorf("parse upload endpoint: %w", errParse)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("upload endpoint must use https")
	}
	if parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("upload endpoint must contain only a scheme and host")
	}
	if strings.HasPrefix(strings.ToLower(parsed.Hostname()), "tos-s3-") {
		return "", fmt.Errorf("upload endpoint must be a native TOS endpoint, not an S3-compatible endpoint")
	}
	return "https://" + parsed.Host, nil
}

func (u *TOSUploader) UploadFile(ctx context.Context, bucket, objectKey, path string) error {
	checksum, _, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		return errChecksum
	}
	_, errUpload := u.client.PutObjectFromFile(ctx, &tos.PutObjectFromFileInput{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket:          bucket,
			Key:             objectKey,
			ContentType:     "application/zstd",
			ForbidOverwrite: true,
			Meta: map[string]string{
				archiveChecksumMetadataKey: checksum,
			},
		},
		FilePath: path,
		GenericInput: tos.GenericInput{RequestHeader: map[string]string{
			tos.HeaderIfNoneMatch: "*",
		}},
	})
	if errUpload != nil {
		if isTOSObjectConflict(errUpload) {
			return fmt.Errorf("%w for %s: %w", ErrObjectConflict, objectKey, errUpload)
		}
		return fmt.Errorf("put TOS object: %w", errUpload)
	}
	return nil
}

// MatchObject reports whether a remote object has the same size and SHA-256 metadata as a local archive.
func (u *TOSUploader) MatchObject(ctx context.Context, bucket, objectKey, path string) (bool, error) {
	checksum, size, errChecksum := fileSHA256(path)
	if errChecksum != nil {
		return false, errChecksum
	}
	output, errHead := u.client.HeadObjectV2(ctx, &tos.HeadObjectV2Input{
		Bucket: bucket,
		Key:    objectKey,
	})
	if errHead != nil {
		return false, fmt.Errorf("head TOS object: %w", errHead)
	}
	if output == nil {
		return false, fmt.Errorf("head TOS object returned no output")
	}
	if output.ContentLength != size || output.Meta == nil {
		return false, nil
	}
	remoteChecksum, exists := output.Meta.Get(archiveChecksumMetadataKey)
	if !exists {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(remoteChecksum), checksum), nil
}

func isTOSObjectConflict(err error) bool {
	var serverError *tos.TosServerError
	if !errors.As(err, &serverError) {
		return false
	}
	switch serverError.Code {
	case codes.PreconditionFailed:
		return serverError.StatusCode == 412
	case codes.DuplicateObject:
		return serverError.StatusCode == 409
	default:
		return false
	}
}

func fileSHA256(path string) (string, int64, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", 0, fmt.Errorf("open archive for checksum: %w", errOpen)
	}
	hash := sha256.New()
	size, errCopy := io.Copy(hash, file)
	errClose := file.Close()
	if errCombined := errors.Join(errCopy, errClose); errCombined != nil {
		return "", 0, fmt.Errorf("checksum archive: %w", errCombined)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), size, nil
}
