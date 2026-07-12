package httpclient

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseBestIPs_ShouldSplitPlusSeparatedValues(t *testing.T) {
	assert.Equal(t, []string{"1.2.3.4", "5.6.7.8"}, parseBestIPs("1.2.3.4+5.6.7.8"))
	assert.Nil(t, parseBestIPs(""))
}

func TestConvertMapToSlice_ShouldCollectKeys(t *testing.T) {
	m := &sync.Map{}
	m.Store("1.1.1.1", struct{}{})
	m.Store("2.2.2.2", struct{}{})
	assert.ElementsMatch(t, []string{"1.1.1.1", "2.2.2.2"}, convertMapToSlice(m))
}

func TestCreateResolver_ShouldReturnResolver(t *testing.T) {
	assert.NotNil(t, createResolver("localhost", time.Second))
	assert.NotNil(t, createResolver("8.8.8.8", time.Second))
}

func TestParseServerResponse_ShouldValidateRetInfo(t *testing.T) {
	records, err := parseServerResponse([]byte(`{"ret_info":{"code":0,"msg":"ok"},"records":[{"domain":"a.com","best_ips":"1.1.1.1","success":true}]}`))
	assert.NoError(t, err)
	assert.Len(t, records, 1)

	_, err = parseServerResponse([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	assert.Error(t, err)
}