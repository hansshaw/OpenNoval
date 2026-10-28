# Android 手机创作工作台

安卓工程将现有 Go 创作引擎编译成 AAR，由原生 Android 前台服务在应用进程内启动。WebView 加载应用自己的本机界面，不需要电脑、远程工作台或额外安装 Go/Node。手动写作与阅读可离线使用；AI 共创、章节生成等功能需要在应用中配置模型连接和 API Key。

## 当前状态

当前实现版本为 `0.1.1`（versionCode `2`）。工程具备原生文件选择与导出、手机导航/章节分区、应用私有存储及 Go 内嵌运行时。本版调整后台创作与退出行为：普通返回可退到后台，活跃模型任务由前台服务和 CPU 唤醒锁保护，从最近任务列表划掉应用则停止任务；手机阅读改为正文优先，阅读工具按需展开。

本版已完成三种 ABI 的实际发行构建，v2/v3 签名、ZIP 16 KB 对齐、64 位原生库 LOAD 段 16 KB 对齐检查通过，Android lint 为 0 错误。Go 全量测试、vet、移动桥接/工作台/队列/host 竞态测试、211 项 JavaScript 测试和 5 项 Java 电源保护测试通过。

最终签名 APK 在 Android 15 x86_64 模拟器上通过实际触摸阅读菜单、目录选章、滑动、返回书架，以及普通返回继续运行、划掉最近任务取消请求并在重开后不自动重试的检查。通过系统弹窗允许后台运行后，70 秒本地模拟模型的流式请求在切换应用、锁屏并强制 Doze 时完整完成；恢复前已保存结果，结束后唤醒锁释放。这些结果不等于实体手机、厂商省电策略、多小时运行或真实服务商长篇生成验收，详细范围见对应 Release。

## 功能与数据

- 复用项目管理、手动章节编辑、AI 共创、规划审核、批次生成、章节版本和阅读器。
- 导入通过系统文件选择器；TXT、EPUB 及媒体导出通过系统“保存到”对话框，无需申请整个存储空间权限。
- 手机底部导航和侧栏抽屉；章节目录、正文、编辑与指令可分别查看。软键盘打开时收起底部导航。
- 手机阅读默认以正文为主。轻点正文中央或底部“菜单”，可展开字号、纸张配色、行距等阅读设置；目录、全文搜索与书签集中在抽屉面板中。
- Android 返回键先关闭当前对话框、阅读菜单或导航。阅读中返回会先收起菜单，再回到书架；普通最终返回将应用退到后台，不停止正在运行的创作任务。从最近任务列表划掉应用会取消 Go 任务并停止本机服务。
- 作品、配置、API Key、风格等位于应用私有目录 `files/studio/`；应用更新通常保留数据，卸载或系统“清除数据”会删除它们。请定期导出正文。当前 TXT/EPUB 导出不是包含所有项目配置、历史与媒体的完整备份，也不自动同步桌面作品。
- 外观、阅读偏好与界面输入保存到应用私有存储，不受本机服务端口变化影响。
- 安卓章节编辑器为未保存修改保留本机草稿。正式版本未变时可恢复编辑；版本已变化时提供明确的载入/丢弃选项，恢复草稿不会自动覆盖已保存正文。备份有短暂延迟，完成重要段落后仍应点击保存。
- 前台服务负责持有 Go 运行时并显示任务通知；活跃模型任务期间持有 CPU 唤醒锁，没有活跃任务时释放唤醒锁。切换应用或锁屏后，任务由服务继续执行。

本机服务只监听 `127.0.0.1`，使用随机会话凭据、HttpOnly Cookie 和请求来源检查。WebView 仅加载当前本机服务资源，外部链接交由浏览器打开。模型请求仍由 Go 后端访问用户配置的服务商。

## 后台运行设置

打开 **模型与连接设置 → 后台运行设置**，查看电池后台运行和任务通知状态。若尚未允许后台运行，可点击“允许后台运行”，再按系统提示手动允许本应用忽略电池优化。应用不会自动修改这项系统授权；任务通知也提供此设置入口。

部分手机还需在应用的系统电池设置中选择“不限制”或允许后台活动，并保留任务通知。CPU 唤醒锁用于活跃任务的执行，锁屏后的联网仍受 Android 及厂商电池策略影响。

普通返回或切换应用可保留后台任务；从最近任务列表划掉应用会取消运行中及排队任务，重新打开不会自动执行这些排队任务。在系统设置中强制停止，或触及系统后台运行限额也会停止任务。前台服务和电池优化授权不保证任务永不中断；已经保存的作品会保留，重新打开应用后检查任务状态再继续。

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
bash mobile/scripts/test-work-protection.sh
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
  --apk output/android/ainovel-android-0.1.1-universal.apk
```

脚本通过系统无障碍树操作安装后的界面，创建独立测试作品，不清除已有作品，不使用模型密钥；结果、截图和日志保存在 `output/android-smoke/`。测试结束可执行 `adb -s emulator-5554 emu kill` 关闭模拟器。

还应在已构建 APK 的真机/模拟器完成：

1. 离线新建手动作品，创建章节、修改保存，停止应用后重新打开检查正文。
2. 输入未保存草稿，切换应用/旋转屏幕，检查草稿恢复与保存冲突提示。
3. 配置一个可用模型服务，执行共创、规划审核及至少一章生成，检查暂停/继续、后台通知，以及活跃任务持有 CPU 唤醒锁、任务结束后释放锁。
4. 从系统文件选择器导入 TXT/EPUB，导出到“下载”或云盘，取消导出后再次尝试。
5. 在小屏、横屏、大字体及软键盘打开时，检查章节编辑、设置对话框和返回键；阅读时检查正文中央点击/底部菜单、字号/纸色/行距、目录搜索书签抽屉，以及返回键收起菜单或回到书架。
6. 在有活跃任务时切换应用、普通返回或锁屏，核对任务继续运行；打开“后台运行设置”，分别检查电池优化授权前后的系统提示和通知状态。
7. 从最近任务列表划掉应用，核对 Go 任务取消、前台服务停止且唤醒锁释放；检查系统停止服务后的提示及重新打开后的已保存数据。验证目标 Android 版本、厂商后台策略和 16 KB 页面设备。

界面保持原有开源许可入口。新增 Go 绑定依赖的原始许可随应用后端嵌入，见 [第三方声明](../LICENSES/THIRD-PARTY.md)。
