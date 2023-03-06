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

package shmipc

import (
	"testing"
)

func TestLogColor(t *testing.T) {
	SetLogLevel(levelTrace)

	internalLogger.tracef("this is tracef %s", "hello world")
	internalLogger.trace("this is trace")

	internalLogger.infof("this is infof %s", "hello world")
	internalLogger.info("this is info")

	internalLogger.debugf("this is debugf %s", "hello world")
	internalLogger.debug("this is debug")

	internalLogger.warnf("this is warnf %s", "hello world")
	internalLogger.warn("this is warn")

	internalLogger.errorf("this is errorf %s", "hello world")
	internalLogger.error("this is error")
}
