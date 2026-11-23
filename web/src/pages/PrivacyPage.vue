<script setup>
import PublicLayout from './PublicLayout.vue'

const origin = location.origin
</script>

<template>
  <PublicLayout
    eyebrow="透明说明 · 数据由你掌控"
    title="隐私政策"
    intro="这份政策说明鱼助手会处理哪些数据、为什么处理，以及数据会留在哪里。鱼助手是自托管工具，核心经营数据默认保存在部署者自己的服务器和数据库中，而不是鱼助手项目维护者的云端账号。"
  >
    <template #hero>
      <div class="public-meta-line">
        <span>生效日期：2026 年 11 月 23 日</span>
        <span>适用服务：{{ origin }}</span>
        <span>版本：鱼助手 1.x</span>
      </div>
    </template>

    <section class="public-trust" aria-label="隐私摘要">
      <article><b>核心数据不集中上云</b><span>交易、截图、闲鱼登录态和备份由部署者自行保存。</span></article>
      <article><b>本地 OCR 不上传图片</b><span>默认识别在浏览器内完成；只有主动启用视觉模型时才会外发。</span></article>
      <article><b>没有广告或追踪 SDK</b><span>当前代码未接入统计、广告、热力图或行为回放脚本。</span></article>
    </section>

    <article class="public-card">
      <nav class="public-toc" aria-label="隐私政策目录">
        <a href="#scope">适用范围</a>
        <a href="#data">收集的数据</a>
        <a href="#purposes">使用目的</a>
        <a href="#third-party">第三方服务</a>
        <a href="#storage">保存与删除</a>
        <a href="#rights">你的权利</a>
        <a href="#contact">联系与更新</a>
      </nav>

      <section class="legal-section" id="scope">
        <h2>1. 适用范围与责任主体</h2>
        <p>本政策适用于当前域名或内网地址下运行的鱼助手网页、管理界面、PWA 和配套浏览器扩展。</p>
        <p>鱼助手是开源、自托管软件。实际部署服务器、域名、账号、备份和运维支持由部署运营者负责；项目维护者通常不会收到具体部署中的经营数据。若你没有自行部署，而是通过他人提供的地址使用鱼助手，应向该地址的运营者提出数据查询、更正或删除请求。</p>
        <p>本页面会展示当前访问地址，便于你确认正在使用的部署。页面不虚构运营者名称、邮箱或其他未在项目中配置的联系方式。</p>
      </section>

      <section class="legal-section" id="data">
        <h2>2. 我们处理哪些数据</h2>
        <p>下表按实际功能盘点，不包含项目中不存在的广告画像、通讯录、精确定位或支付卡数据。</p>
        <div class="legal-table-wrap" tabindex="0" role="region" aria-label="数据处理清单，可横向滚动">
          <table class="legal-table">
            <thead><tr><th>功能</th><th>处理的数据</th><th>保存位置</th></tr></thead>
            <tbody>
              <tr><td><b>登录与店铺选择</b></td><td>管理密码、店铺密码、只读密码；服务端只保存 scrypt 加盐哈希，不保存明文</td><td>自有服务器 SQLite</td></tr>
              <tr><td><b>交易与联单</b></td><td>商品名称、买卖双方昵称、订单号、成交价、运费、平台费用、时间、备注、归属人、联单及分摊结果</td><td>自有服务器 SQLite</td></tr>
              <tr><td><b>寄卖与代采</b></td><td>寄卖人/委托人姓名、代采商品、金额、结算记录和备注</td><td>自有服务器 SQLite</td></tr>
              <tr><td><b>截图与识别</b></td><td>你上传的订单截图、缩略图、识别结果及“准确/需修正”反馈</td><td>截图在自有服务器；默认 OCR 在浏览器本地运行</td></tr>
              <tr><td><b>闲鱼同步</b></td><td>闲鱼/淘宝域的登录 Cookie、昵称、店铺绑定关系、同步状态和错误信息</td><td>自有服务器 SQLite</td></tr>
              <tr><td><b>配置与备份</b></td><td>OCR 接口和模型名、API Key、更新代理、备份计划、导出文件及备份文件</td><td>自有服务器或部署者指定目录</td></tr>
              <tr><td><b>浏览器本地状态</b></td><td>当前店铺、主题、动效偏好、表格列设置、安装提示、更新提示和未提交录单草稿</td><td>当前浏览器的 localStorage/sessionStorage</td></tr>
              <tr><td><b>浏览器扩展</b></td><td>扩展设置、服务器地址、店铺 ID、后台任务状态及从闲鱼页面读取的登录 Cookie</td><td>浏览器扩展本地存储；登录态发送到你配置的服务器</td></tr>
            </tbody>
          </table>
        </div>
        <div class="legal-callout">录单草稿可能包含商品、订单号、对方昵称、金额和备注，会暂存在当前浏览器，直到被清除。共用电脑时，请在离开前清除草稿并退出登录。</div>
      </section>

      <section class="legal-section" id="cookies">
        <h2>3. Cookie、本地存储与登录状态</h2>
        <p>开启访问密码后，服务端会设置名为 <code>xy_session</code> 的必要 Cookie，用于识别登录会话。它设置为 HttpOnly、SameSite=Lax，在 HTTPS 下启用 Secure，最长有效期为 7 天。它不用于广告或跨站追踪。</p>
        <p>主题、当前店铺、排序、列显示、更新检查和安装提示等偏好保存在浏览器本地存储中。禁用本地存储可能使部分偏好无法保存，但不影响核心记账功能。</p>
        <p>浏览器扩展只有在用户安装、授权并配置自建服务器后才读取 goofish.com、taobao.com 和 tmall.com 的相关 Cookie。扩展可选访问用户指定的服务器域名，不会把 Cookie 发送到项目维护者或广告平台。</p>
        <p>当前代码没有接入 Google Analytics、广告像素、热力图、会话回放或其他非必要追踪脚本，因此没有在页面加载前偷偷启动统计或营销脚本。若未来部署者自行加入第三方追踪，应由该部署者更新政策并提供相应同意机制。</p>
      </section>

      <section class="legal-section" id="purposes">
        <h2>4. 使用这些数据的目的</h2>
        <ul>
          <li>完成登录验证、店铺隔离和只读权限控制，保护经营数据不被误改。</li>
          <li>记录买卖、联单、寄卖、代采和结算，计算成本、费用、销量与真实利润。</li>
          <li>使用你主动上传的截图进行文字识别，并在本地规则下提取金额、订单号和时间。</li>
          <li>通过你授权的闲鱼登录态同步订单、费用和物流信息，减少手工录入。</li>
          <li>提供导入导出、自动备份、撤销误操作、故障排查和在线更新。</li>
          <li>保存界面偏好和未提交草稿，让下一次打开更接近上次工作状态。</li>
        </ul>
        <p>鱼助手不会出售个人数据，不会将经营数据用于广告画像，也不会把数据用于与账本功能无关的自动化决策。</p>
      </section>

      <section class="legal-section" id="third-party">
        <h2>5. 第三方服务与数据外发</h2>
        <h3>闲鱼及阿里巴巴相关服务</h3>
        <p>启用闲鱼同步、扫码登录或订单验证时，服务器或扩展会访问闲鱼、淘宝或天猫接口，并携带相应登录态。这个过程受这些平台自己的服务条款和隐私规则约束。鱼助手与这些平台不存在隶属或官方背书关系。</p>
        <h3>可选视觉 OCR</h3>
        <p>使用默认本地 OCR 时，OCR 模型在浏览器 WASM 中运行，图片不会因识别而上传。只有你主动切换到“视觉大模型”并配置 OpenAI 兼容接口时，订单截图才会通过鱼助手后端代理发送到你选择的模型服务。该服务如何处理图片、日志和训练数据，以其政策为准。</p>
        <h3>更新检查</h3>
        <p>服务端检查更新时会访问项目公开的 GitHub Release，并对请求携带软件版本标识；不需要 GitHub Token。若部署者配置了代理，代理运营者可能看到请求地址等连接信息。更新只获取公共版本和发布包，不上传经营记录。</p>
        <h3>备份、反向代理与托管基础设施</h3>
        <p>如果部署者把备份同步到网盘、对象存储或另一台服务器，或通过 CDN、反向代理、隧道提供访问，这些基础设施运营者可能接触传输内容和访问日志。鱼助手代码无法控制其留存政策。</p>
      </section>

      <section class="legal-section" id="security">
        <h2>6. 数据安全</h2>
        <ul>
          <li>密码使用 scrypt 加盐哈希保存；会话 Cookie 使用 HttpOnly、SameSite 和 HTTPS 下的 Secure 标记。</li>
          <li>服务端包含 CSP、X-Frame-Options、nosniff、严格 Referrer-Policy 和请求超时等基础防护。</li>
          <li>导出文件不包含访问密码和 OCR API Key；数据库、备份和 API Key 不应提交到公开仓库。</li>
          <li>闲鱼 Cookie 属于敏感登录凭证，应仅在你自己控制的服务器中保存，并限制服务器和备份目录权限。</li>
        </ul>
        <p>任何系统都无法承诺绝对安全。请使用 HTTPS、强密码、受控网络和定期备份；不要把公开链接与默认密码一起发给无关人员。</p>
      </section>

      <section class="legal-section" id="storage">
        <h2>7. 保存期限、备份与删除</h2>
        <p>经营记录会一直保存在部署者的数据库中，直到你主动删除、覆盖数据库、卸载服务或由部署运营者按运维规则清理。系统不设置统一的自动数据过期期限。</p>
        <p>登录会话最长 7 天。浏览器偏好保留到浏览器数据被清除或你修改设置。自动备份按部署者设置的小时数和保留份数轮换；被删除的主数据可能仍存在于历史备份、撤销快照或部署者的异地备份中，需要由部署运营者按其备份策略进一步清理。</p>
        <p>删除截图、记录或备份后，新的请求通常不再展示对应数据。若同一数据已出现在你自行下载的导出文件或第三方备份中，仍需从这些副本中删除。</p>
      </section>

      <section class="legal-section" id="rights">
        <h2>8. 你的选择与权利</h2>
        <ul>
          <li><b>访问和导出：</b>在“设置 → 数据导出/导入”中导出当前店铺的 JSON、CSV 或 Excel 数据。</li>
          <li><b>更正：</b>在明细或录单弹窗中修正订单、金额、归属人、费用和备注。</li>
          <li><b>删除：</b>删除交易、附件、寄卖记录或备份；部分删除支持短时撤销。</li>
          <li><b>撤回同步授权：</b>在闲鱼退出登录、清除服务器绑定、卸载浏览器扩展或停用自动同步。</li>
          <li><b>清除本地状态：</b>使用浏览器站点数据清理功能移除偏好和未提交草稿。</li>
          <li><b>请求协助：</b>向当前部署的运营者申请查询、更正、导出或删除。若数据由你本人部署，你同时是数据控制者和运维者。</li>
        </ul>
      </section>

      <section class="legal-section" id="children">
        <h2>9. 未成年人使用</h2>
        <p>鱼助手面向个人卖家、小微商家及负责经营记账的成年人。未成年人应在监护人知情和同意下使用，不应上传与经营无关的敏感个人信息。</p>
      </section>

      <section class="legal-section" id="contact">
        <h2>10. 政策更新与联系</h2>
        <p>功能、第三方服务或保存方式发生变化时，本政策会同步更新，并在本页标注生效日期。重大变化应通过部署公告或其他合理方式提示。</p>
        <p>项目未提供具体运营者名称和联系邮箱，因此本页不虚构联系方式。请通过向你提供鱼助手访问地址、部署服务或运维支持的渠道联系数据负责人。开源代码问题可通过项目仓库的 Issue 渠道反馈，但不要在公开 Issue 中粘贴密码、Cookie、API Key、订单截图或真实客户资料。</p>
        <div class="legal-callout">这是一份根据当前代码生成的产品隐私说明初稿，不替代针对具体司法辖区、商业规模或敏感行业的专项法律意见。</div>
      </section>
    </article>

    <div class="public-actions">
      <a class="public-action primary" href="/">进入经营账本</a>
      <a class="public-action" href="/terms">查看服务条款</a>
    </div>
  </PublicLayout>
</template>
