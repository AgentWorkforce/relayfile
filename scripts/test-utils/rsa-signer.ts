import { createHash, generateKeyPairSync, sign as cryptoSign } from 'node:crypto';
import { createServer as createHttpServer, type Server } from 'node:http';

type PublicJwk = {
  kty?: string;
  n?: string;
  e?: string;
};

export type LocalRs256Auth = {
  jwksUrl: string;
  generateToken: (workspaceId: string, agentName: string, scopes: string[], expSeconds?: number) => string;
  close: () => Promise<void>;
};

function base64urlJson(value: unknown): string {
  return Buffer.from(JSON.stringify(value)).toString('base64url');
}

function closeServer(server: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((err) => {
      if (err) reject(err);
      else resolve();
    });
  });
}

export async function createLocalRs256Auth(): Promise<LocalRs256Auth> {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const publicJwk = publicKey.export({ format: 'jwk' }) as PublicJwk;

  if (!publicJwk.n || !publicJwk.e) {
    throw new Error('Generated RSA public key is missing JWK modulus or exponent');
  }

  const jwk = { e: publicJwk.e, kty: 'RSA', n: publicJwk.n };
  const kid = createHash('sha256')
    .update(JSON.stringify(jwk))
    .digest('base64url');
  const jwksBody = JSON.stringify({
    keys: [{ ...jwk, alg: 'RS256', use: 'sig', kid }],
  });

  const jwksServer = createHttpServer((_req, res) => {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(jwksBody);
  });

  await new Promise<void>((resolve, reject) => {
    jwksServer.once('error', reject);
    jwksServer.listen(0, '127.0.0.1', () => {
      jwksServer.off('error', reject);
      resolve();
    });
  });

  const addr = jwksServer.address();
  if (!addr || typeof addr === 'string') {
    await closeServer(jwksServer);
    throw new Error('JWKS server did not bind to a TCP port');
  }

  return {
    jwksUrl: `http://127.0.0.1:${addr.port}/.well-known/jwks.json`,
    generateToken: (workspaceId, agentName, scopes, expSeconds = 3600) => {
      const header = { alg: 'RS256', typ: 'JWT', kid };
      const payload = {
        wks: workspaceId,
        workspace_id: workspaceId,
        sub: agentName,
        agent_name: agentName,
        scopes,
        exp: Math.floor(Date.now() / 1000) + expSeconds,
        aud: 'relayfile',
      };
      const signingInput = `${base64urlJson(header)}.${base64urlJson(payload)}`;
      const signature = cryptoSign('RSA-SHA256', Buffer.from(signingInput), privateKey).toString('base64url');
      return `${signingInput}.${signature}`;
    },
    close: () => closeServer(jwksServer),
  };
}
