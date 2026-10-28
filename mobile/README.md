# Android 手机创作工作台

安卓工程将现有 Go 创作引擎编译成 AAR，由原生 Android 前台服务在应用进程内启动。WebView 加载应用自己的本机界面，不需要电脑、远程工作台或额外安装 Go/Node。手动写作与阅读可离线使用；AI 共创、章节生成等功能需要在应用中配置模型连接和 API Key。

## 当前状态

已实现 Android 工程、原生文件选择与导出、手机导航/章节分区、应用私有存储及 Go 内嵌运行时。Go 集成测试验证新建作品、编辑章节、TXT/EPUB 导出、模型配置、重启持久化及访问鉴权；Java 源码通过独立编译检查，Go 代码通过 Android arm64/arm/amd64 目标编译检查。Chromium 的 390×844、360×740 手机视口验证了建书、保存正文、阅读、导航及设置，无页面脚本异常或横向溢出。

已使用真实 Android SDK/NDK 构建 Release APK，完成 v2/v3 发行签名、ZIP 16 KB 对齐、64 位原生库 16 KB LOAD 段对齐和 Android lint 检查（0 错误）。应用版本为 `0.1.0`（versionCode `1`）。模拟器与真机验证范围以对应 Release 说明为准，源码检查不能代替设备验收。

## 功能与数据

- 复用项目管理、手动章节编辑、AI 共创、规划审核、批次生成、章节版本和阅读器。
- 导入通过系统文件选择器；TXT、EPUB 及媒体导出通过系统“保存到”对话框，无需申请整个存储空间权限。
- 手机底部导航和侧栏抽屉；章节目录、正文、编辑与指令可分别查看。软键盘打开时收起底部导航。
- Android 返回键先关闭对话框或导航、返回项目中心；最终退出前确认，并停止本机服务。
- 作品、配置、API Key、风格等位于应用私有目录 `files/studio/`；应用更新通常保留数据，卸载或系统“清除数据”会删除它们。请定期导出正文。当前 TXT/EPUB 导出不是包含所有项目配置、历史与媒体的完整备份，也不自动同步桌面作品。
- 外观、阅读偏好与界面输入保存到应用私有存储，不受本机服务端口变化影响。
- 安卓章节编辑器为未保存修改保留本机草稿。正式版本未变时可恢复编辑；版本已变化时提供明确的载入/丢弃选项，恢复草稿不会自动覆盖已保存正文。备份有短暂延迟，完成重要段落后仍应点击保存。
- 后台服务使用常驻通知。安卓系统省电、进程回收或后台运行限额仍可能中断长任务；已保存的数据保留，重新打开后检查任务状态再继续。

本机服务只监听 `127.0.0.1`，使用随机会话凭据、HttpOnly Cookie 和请求来源检查。WebView 仅加载当前本机服务资源，外部链接交由浏览器打开。模型请求仍由 Go 后端访问用户配置的服务商。

## 构建环境

| 工具 | 固定版本或要求 |
| --- | --- |
| Go | 1.26 或更新；手机绑定模块独立于桌面模块 |
| JDK | 17 |
| Gradle | 8.11.1 |
| Android Gradle Plugin | 8.9.1 |
| Android SDK | Platform 35、Build Tools 35.0.0、Platform Tools |
| Android NDK | 28.0.13004108 |
| gomobile / gobind | `v0.0.0-20261011034528-197770444867` |

应用最低 Android 8.0（API 26），target SDK 35。默认构建包含 arm64-v8a、armeabi-v7a 和 x86_64。实际兼容性需要在相应设备验收，并保持 Android System WebView 更新。

先安装 JDK、Gradle 和 [Android 命令行工具](https://developer.android.com/studio#command-tools)。阅读并接受 [Google Android SDK 许可协议](https://developer.android.com/studio/terms)，再安装 SDK/NDK。构建脚本不会自动替用户接受协议。

```bash
export JAVA_HOME=/你的/jdk-17
export ANDROID_HOME=/你的/Android/Sdk
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/cmdline-tools/latest/bin:/你的/gradle-8.11.1/bin:$PATH"

sdkmanager --licenses
sdkmanager "platforms;android-35" "build-tools;35.0.0" "ndk;28.0.13004108" "platform-tools"

# 在仓库根目录运行
bash mobile/scripts/setup-go-tools.sh
npm run android:doctor
npm run android:build
```

脚本也会查找本仓库 `output/android-toolchain/` 中的 JDK、Gradle、SDK 和 Go 绑定工具。环境变量可覆盖这些默认位置。`android:doctor` 会列出缺少的工具并以非零状态退出。

成功构建后，**用于本地安装测试的调试签名 APK** 输出到：

```text
output/android/AInovel-debug.apk
```

如只需现代 ARM 手机，可缩小编译范围：

```bash
AINOVEL_ANDROID_TARGETS=android/arm64 npm run android:build
```

通过 USB 安装：

```bash
adb install -r output/android/AInovel-debug.apk
```

也可将 APK 复制到手机后打开，并按系统提示授权对应文件应用安装。调试版用于测试，Release 附件采用持续复用的发行签名；不同签名不能直接覆盖安装。

首次建立本地发行签名，并构建可安装的发行 APK：

```bash
source mobile/scripts/env.sh
python3 mobile/scripts/init-signing-key.py
npm run android:release
```

签名目录默认为 `~/.local/share/ainovel/android-signing/`，也可设置 `AINOVEL_SIGNING_DIR`。其中的 `ainovel-release.p12` 和 `keystore-password` 必须一起私密备份；已有身份不会被脚本覆盖，后续版本须继续使用相同签名。私钥和密码不进入 Git 或 Release。正式 APK 输出为 `output/android/ainovel-android-<版本>-universal.apk`，脚本自动验签并检查 ZIP 对齐。

自行运行 `bash mobile/scripts/build-apk.sh release` 只构建未签名 APK，可再调用 `bash mobile/scripts/sign-release.sh` 签名。已有公司或个人发行密钥的维护者，应使用自己的签名流程；不要为已发布应用重新生成身份。构建所需的 Google Maven 访问若受网络影响，可通过 `AINOVEL_GRADLE_INIT_SCRIPT` 指定本机 Gradle 仓库配置，不改变项目默认依赖来源。

## 验证

```bash
go test ./...
go vet ./...
go test -race ./internal/mobilebridge ./internal/entry/desktopui ./internal/host
npm test
```

`bash mobile/scripts/check-java.sh /路径/android-all.jar` 可使用已下载的
`org.robolectric:android-all:15-robolectric-13954326` 检查 Java API 与实际生成的
Go Java 绑定。该检查不下载依赖，也不生成资源、JNI 库或 APK。

在支持 KVM 的 Linux 主机上，可使用 Android 15 模拟器检查实际发行包。先安装并接受对应系统镜像许可，然后在一个终端保持模拟器运行：

```bash
source mobile/scripts/env.sh
sdkmanager "emulator" "system-images;android-35;google_apis;x86_64"
AINOVEL_EMULATOR_STAY_ALIVE=1 bash mobile/scripts/start-emulator.sh
```

在另一个终端执行：

```bash
source mobile/scripts/env.sh
python3 mobile/scripts/android-smoke.py \
  --serial emulator-5554 \
  --apk output/android/ainovel-android-0.1.0-universal.apk
```

脚本通过系统无障碍树操作安装后的界面，创建独立测试作品，不清除已有作品，不使用模型密钥；结果、截图和日志保存在 `output/android-smoke/`。测试结束可执行 `adb -s emulator-5554 emu kill` 关闭模拟器。

还应在已构建 APK 的真机/模拟器完成：

1. 离线新建手动作品，创建章节、修改保存，退出再打开检查正文。
2. 输入未保存草稿，切换应用/旋转屏幕，检查草稿恢复与保存冲突提示。
3. 配置一个可用模型服务，执行共创、规划审核及至少一章生成，检查暂停/继续和后台通知。
4. 从系统文件选择器导入 TXT/EPUB，导出到“下载”或云盘，取消导出后再次尝试。
5. 在小屏、横屏、大字体及软键盘打开时，检查章节编辑、设置对话框和返回键。
6. 检查前台服务被系统停止后的提示与重新打开后的恢复；验证目标 Android 版本和 16 KB 页面设备。

界面保持原有开源许可入口。新增 Go 绑定依赖的原始许可随应用后端嵌入，见 [第三方声明](../LICENSES/THIRD-PARTY.md)。
