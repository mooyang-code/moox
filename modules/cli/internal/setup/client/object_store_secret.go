package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// ObjectStoreCredential 是归档对象存储的 S3 访问密钥（Access Key ID / Secret Access Key）。
type ObjectStoreCredential struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Env 渲染成 worker 的凭据文件 archive-object-store.env（只有两个键，值里没有空白）。
func (c ObjectStoreCredential) Env() []byte {
	return []byte("ACCESS_KEY_ID=" + c.AccessKeyID + "\nSECRET_ACCESS_KEY=" + c.SecretAccessKey + "\n")
}

// ObjectStoreSecret 从 Admin 读出归档对象存储的密钥（管理台“设置 › 密钥”里那条）：要求提供方是 s3（不区分大小写）、
// 状态 active、标识与秘钥值非空且不含空白；不校验分类。错误信息不含密钥的任何值。
func (c *Client) ObjectStoreSecret(ctx context.Context, secretID string) (ObjectStoreCredential, error) {
	if strings.TrimSpace(secretID) == "" {
		return ObjectStoreCredential{}, errors.New("archive_object_store.secret_id 为空")
	}
	rsp := &pb.GetSecretValueRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SecretMgr", "GetSecretValue", &pb.GetSecretValueReq{SecretId: secretID}, rsp); err != nil {
		return ObjectStoreCredential{}, err
	}
	if err := remoteRet("GetSecretValue", rsp.GetRetInfo()); err != nil {
		return ObjectStoreCredential{}, err
	}
	return objectStoreCredential(secretID, rsp.GetSecret())
}

func objectStoreCredential(secretID string, secret *pb.SecretMaterial) (ObjectStoreCredential, error) {
	if secret == nil {
		return ObjectStoreCredential{}, fmt.Errorf("密钥 %s 没有返回内容", secretID)
	}
	if !strings.EqualFold(strings.TrimSpace(secret.GetProvider()), "s3") {
		return ObjectStoreCredential{}, fmt.Errorf("密钥 %s 的提供方不是 s3：归档对象存储的密钥要在管理台按 提供方 s3、类型 API 密钥对 录入", secretID)
	}
	if secret.GetStatus() != "active" {
		return ObjectStoreCredential{}, fmt.Errorf("密钥 %s 没有启用", secretID)
	}
	keyID, value := secret.GetKeyId(), secret.GetSecretValue()
	if keyID == "" || value == "" || strings.ContainsAny(keyID, " \t\r\n\"'") || strings.ContainsAny(value, " \t\r\n\"'") {
		return ObjectStoreCredential{}, fmt.Errorf("密钥 %s 的标识或秘钥值为空或含空白、引号", secretID)
	}
	return ObjectStoreCredential{AccessKeyID: keyID, SecretAccessKey: value}, nil
}
